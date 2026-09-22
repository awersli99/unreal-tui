package main

import (
	"cmp"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"
	"uuid"

	"github.com/unreallabsai/unreal-agent/harness/contextbuilder"
	"github.com/unreallabsai/unreal-agent/harness/coordinator"
	"github.com/unreallabsai/unreal-agent/harness/inbox"
	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/operation"
	"github.com/unreallabsai/unreal-agent/harness/session"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore/localfile"
	"github.com/unreallabsai/unreal-agent/harness/tool"
	"github.com/unreallabsai/unreal-agent/harness/tool/bash"
	"github.com/unreallabsai/unreal-agent/harness/tool/viewimage"
)

const (
	interruptReason       = "Interrupted by user"
	switchReason          = "Switching session"
	exitReason            = "Exiting"
	toolHeartbeatInterval = 10 * time.Minute
	historyPageSize       = 256
	closeTimeout          = 5 * time.Second
)

// ItemEvent reports an item persisted to a session.
type ItemEvent struct {
	SessionID session.ID
	Item      sessionstore.Item
}

// RunEndedEvent reports that a coordinator exited.
type RunEndedEvent struct {
	Err error
}

type EngineConfig struct {
	StoreDirectory string
	Workspace      string
	Client         Client
	Model          string
	Effort         llm.ReasoningEffort
	SystemPrompt   string
	Skills         []tool.Skill
	Shell          string
	Emit           func(any)
}

// Engine drives the harness coordinator for one interactive session at a time.
//
// A coordinator runs while there is work: it starts when the user sends a
// message and keeps running (it never receives StopWhenIdle), so later messages
// go straight into its inbox and steer the agent mid-turn. Interrupting submits
// StopHard, which cancels the model call and running tools; the next message
// starts a fresh coordinator that resumes the persisted session.
//
// The model, client, and system prompt are read at every model call (see
// liveBuilder and liveAdapter), so switching them never interrupts work.
type Engine struct {
	ctx            context.Context
	store          *localfile.Store
	storeDirectory string
	workspace      string
	shell          string
	display        tool.Registry
	events         *eventQueue

	// live guards the values read at every model call.
	live   sync.Mutex
	client Client
	model  string
	prompt string

	mu             sync.Mutex
	skills         []tool.Skill
	effort         llm.ReasoningEffort
	sessionID      session.ID
	recordedEffort llm.ReasoningEffort
	run            *engineRun
	queued         []inbox.Input
	closed         bool
}

type engineRun struct {
	ctx      context.Context
	inbox    *inbox.Inbox
	stopping bool
	done     chan struct{}
}

type SessionSummary struct {
	ID        session.ID
	UpdatedAt time.Time
	Title     string
}

func NewEngine(ctx context.Context, config EngineConfig) (*Engine, error) {
	if err := os.MkdirAll(config.StoreDirectory, 0o700); err != nil {
		return nil, fmt.Errorf("create session directory: %w", err)
	}
	store, err := localfile.New(config.StoreDirectory)
	if err != nil {
		return nil, fmt.Errorf("open session store: %w", err)
	}
	engine := &Engine{
		ctx:            ctx,
		store:          store,
		storeDirectory: config.StoreDirectory,
		workspace:      config.Workspace,
		shell:          config.Shell,
		prompt:         config.SystemPrompt,
		skills:         config.Skills,
		events:         newEventQueue(),
		client:         config.Client,
		model:          config.Model,
		effort:         config.Effort,
	}
	engine.display, err = engine.newRegistry(filepath.Join(config.StoreDirectory, "operations"))
	if err != nil {
		return nil, err
	}
	// The store forbids adding observers concurrently with writes, so the
	// single observer is registered before any coordinator exists.
	store.AddObserver(func(id session.ID, item sessionstore.Item) {
		engine.events.push(ItemEvent{SessionID: id, Item: item})
	})
	go engine.events.pump(ctx, config.Emit)
	return engine, nil
}

// ResultTranslator formats recorded tool calls for display, exactly as the
// model sees them.
func (engine *Engine) ResultTranslator(name string) (tool.ResultTranslator, bool) {
	translator, ok := engine.display.Resolve(name)
	return translator, ok
}

func (engine *Engine) SessionID() session.ID {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	return engine.sessionID
}

func (engine *Engine) SessionPath() string {
	id := engine.SessionID()
	if id == "" {
		return ""
	}
	return filepath.Join(engine.storeDirectory, string(id)+".session.jsonl")
}

// Send delivers a user message: straight into the running coordinator's inbox
// (steering), or by starting a coordinator when none is running.
func (engine *Engine) Send(text string) error {
	payload, err := json.Marshal(text)
	if err != nil {
		return fmt.Errorf("encode message: %w", err)
	}
	input := inbox.Input{ID: inbox.ID(uuid.New().String()), Kind: inbox.InputExternal, Payload: payload}

	engine.mu.Lock()
	defer engine.mu.Unlock()
	if engine.closed {
		return errors.New("engine is closed")
	}
	if engine.run != nil {
		if engine.run.stopping {
			// Inputs queued in a stopping coordinator's inbox could be dropped,
			// so hold them until it exits and start the next run with them.
			engine.queued = append(engine.queued, input)
			return nil
		}
		return engine.run.inbox.Submit(engine.run.ctx, input)
	}
	return engine.startLocked([]inbox.Input{input})
}

// Interrupt stops the current turn and running tools. It reports whether a
// coordinator was running.
func (engine *Engine) Interrupt() bool {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	engine.queued = nil
	return engine.stopLocked(interruptReason)
}

// SetEffort changes the reasoning effort for subsequent model calls. The
// harness applies it through a settings control input, so a running
// coordinator picks it up without restarting.
func (engine *Engine) SetEffort(effort llm.ReasoningEffort) error {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	engine.effort = effort
	if engine.run == nil || engine.run.stopping {
		return nil
	}
	input, err := settingsInput(effort)
	if err != nil {
		return err
	}
	if err := engine.run.inbox.Submit(engine.run.ctx, input); err != nil {
		return err
	}
	engine.recordedEffort = effort
	return nil
}

// SetModel switches the model, and the client when non-nil. Both take effect
// at the next model call, even mid-turn; the caller owns client lifetimes.
func (engine *Engine) SetModel(client Client, model string) {
	engine.live.Lock()
	defer engine.live.Unlock()
	engine.model = model
	if client != nil {
		engine.client = client
	}
}

// SetSystemPrompt replaces the system prompt from the next model call on.
func (engine *Engine) SetSystemPrompt(prompt string) {
	engine.live.Lock()
	defer engine.live.Unlock()
	engine.prompt = prompt
}

// SetSkills replaces the skills offered by the next coordinator.
func (engine *Engine) SetSkills(skills []tool.Skill) {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	engine.skills = skills
}

func (engine *Engine) liveModel() (Client, string, string) {
	engine.live.Lock()
	defer engine.live.Unlock()
	return engine.client, engine.model, engine.prompt
}

// NewSession detaches from the current session; the next message creates one.
func (engine *Engine) NewSession() {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	engine.stopLocked(switchReason)
	engine.queued = nil
	engine.sessionID = ""
	engine.recordedEffort = ""
}

// ResumeSession switches to a persisted session and returns its history.
func (engine *Engine) ResumeSession(id session.ID) ([]sessionstore.Item, error) {
	items, err := engine.loadItems(id)
	if err != nil {
		return nil, err
	}
	engine.mu.Lock()
	defer engine.mu.Unlock()
	if id != engine.sessionID {
		engine.stopLocked(switchReason)
		engine.queued = nil
	}
	engine.sessionID = id
	engine.recordedEffort = recordedEffort(items)
	return items, nil
}

// Sessions lists this workspace's sessions, most recently updated first.
func (engine *Engine) Sessions() ([]SessionSummary, error) {
	infos, err := engine.store.ListSessions(engine.ctx)
	if err != nil {
		return nil, err
	}
	summaries := make([]SessionSummary, 0, len(infos))
	for _, info := range infos {
		summary := SessionSummary{ID: info.ID, UpdatedAt: info.LastUpdatedAt}
		page, err := engine.store.Items(engine.ctx, info.ID, sessionstore.BeforeFirst, 32)
		if err != nil {
			continue
		}
		for _, item := range page.Items {
			if text, ok := externalText(item); ok {
				summary.Title = text
				break
			}
		}
		if summary.Title == "" {
			continue // Created but never used.
		}
		summaries = append(summaries, summary)
	}
	slices.SortFunc(summaries, func(left, right SessionSummary) int {
		return right.UpdatedAt.Compare(left.UpdatedAt)
	})
	return summaries, nil
}

// Close stops the coordinator, letting it cancel running tools.
func (engine *Engine) Close() {
	engine.mu.Lock()
	engine.closed = true
	engine.queued = nil
	engine.stopLocked(exitReason)
	run := engine.run
	engine.mu.Unlock()
	if run != nil {
		select {
		case <-run.done:
		case <-time.After(closeTimeout):
		}
	}
}

func (engine *Engine) stopLocked(reason string) bool {
	run := engine.run
	if run == nil || run.stopping {
		return false
	}
	run.stopping = true
	payload, err := json.Marshal(inbox.ControlMessage{Mode: inbox.StopHard, Reason: reason})
	if err == nil {
		err = run.inbox.Submit(run.ctx, inbox.Input{
			ID: inbox.ID(uuid.New().String()), Kind: inbox.InputControl, Payload: payload,
		})
	}
	if err != nil {
		// The coordinator is already gone or cannot take input; its run goroutine
		// still cleans up when Run returns.
		return false
	}
	return true
}

func (engine *Engine) startLocked(inputs []inbox.Input) error {
	ctx := engine.ctx
	if engine.sessionID == "" {
		id := session.ID(uuid.New().String())
		if _, err := engine.store.Create(ctx, id); err != nil {
			return fmt.Errorf("create session: %w", err)
		}
		engine.sessionID = id
		engine.recordedEffort = ""
	}
	id := engine.sessionID

	// No coordinator owns the session right now, so inputs are written to the
	// store directly. On resume the coordinator sees them as undelivered and
	// immediately calls the model with them.
	if engine.effort != engine.recordedEffort {
		settings, err := settingsInput(engine.effort)
		if err != nil {
			return err
		}
		inputs = append([]inbox.Input{settings}, inputs...)
	}
	for _, input := range inputs {
		if err := engine.store.AppendInput(ctx, id, input); err != nil {
			return fmt.Errorf("record input: %w", err)
		}
	}
	engine.recordedEffort = engine.effort

	restored, err := engine.store.Resume(ctx, id)
	if err != nil {
		return fmt.Errorf("resume session %s: %w", id, err)
	}
	operationDirectory := filepath.Join(engine.storeDirectory, "operations", string(id))
	if err := os.MkdirAll(operationDirectory, 0o700); err != nil {
		return fmt.Errorf("create operation directory: %w", err)
	}
	registry, err := engine.newRegistry(operationDirectory)
	if err != nil {
		return err
	}

	runContext, cancel := context.WithCancel(ctx)
	inputsInbox, err := inbox.New(runContext, restored.ExternalInputIDs)
	if err != nil {
		cancel()
		return fmt.Errorf("open inbox: %w", err)
	}
	builder := &liveBuilder{Builder: contextbuilder.NewBuilder(registry.Skills()...), engine: engine}
	builder.SetModel(llm.Model{ReasoningEffort: engine.effort})
	for _, definition := range registry.StaticDefinitions() {
		builder.AddTool(definition.Tool)
	}
	current := coordinator.New(coordinator.Dependencies{
		ToolHeartbeatInterval: toolHeartbeatInterval,
		SessionID:             id,
		Inbox:                 inputsInbox,
		Restored:              restored,
		Sessions:              engine.store,
		ContextBuilder:        builder,
		LLM:                   liveAdapter{engine: engine},
		Tools:                 registry,
		Operations:            operation.NewLocalOperationManager(runContext),
	})

	run := &engineRun{ctx: runContext, inbox: inputsInbox, done: make(chan struct{})}
	engine.run = run
	go func() {
		err := current.Run(runContext)
		cancel()

		engine.mu.Lock()
		engine.run = nil
		close(run.done)
		var restartErr error
		if len(engine.queued) != 0 && !engine.closed {
			queued := engine.queued
			engine.queued = nil
			restartErr = engine.startLocked(queued)
		}
		engine.mu.Unlock()

		engine.events.push(RunEndedEvent{Err: err})
		if restartErr != nil {
			engine.events.push(RunEndedEvent{Err: restartErr})
		}
	}()
	return nil
}

func (engine *Engine) newRegistry(operationDirectory string) (tool.Registry, error) {
	shell := firstNonEmpty(engine.shell, os.Getenv("SHELL"), "/bin/sh")
	names := []string{tool.BashName, tool.ViewImageName}
	if len(engine.skills) != 0 {
		names = append(names, tool.SkillUseName)
	}
	registry := tool.NewRegistry(tool.StaticTranslators{
		Bash: bash.New(bash.Config{
			Shell:         shell,
			Directory:     engine.workspace,
			BaseDirectory: operationDirectory,
		}),
		ViewImage: viewimage.New(viewimage.Config{Directory: engine.workspace}),
	}, names...)
	for _, skill := range engine.skills {
		if _, err := registry.RegisterSkill(skill); err != nil {
			return nil, fmt.Errorf("register skill %q: %w", skill.Path, err)
		}
	}
	return registry, nil
}

func (engine *Engine) loadItems(id session.ID) ([]sessionstore.Item, error) {
	var items []sessionstore.Item
	after := sessionstore.BeforeFirst
	for {
		page, err := engine.store.Items(engine.ctx, id, after, historyPageSize)
		if err != nil {
			return nil, fmt.Errorf("load session %s: %w", id, err)
		}
		items = append(items, page.Items...)
		if !page.More || page.NextAfter <= after {
			return items, nil
		}
		after = page.NextAfter
	}
}

// liveBuilder applies the engine's current model and system prompt each time
// the coordinator builds a request. Build runs on the coordinator goroutine,
// the only one that touches the inner builder.
type liveBuilder struct {
	contextbuilder.Builder
	engine  *Engine
	applied *string
}

func (builder *liveBuilder) Build() (contextbuilder.Result, error) {
	_, model, prompt := builder.engine.liveModel()
	if builder.applied == nil || *builder.applied != prompt {
		builder.Builder.SetSystemPrompt(prompt)
		builder.applied = &prompt
	}
	result, err := builder.Builder.Build()
	result.Request.Model.ID = model
	return result, err
}

// liveAdapter sends each model call through the engine's current client.
type liveAdapter struct {
	engine *Engine
}

func (adapter liveAdapter) Respond(ctx context.Context, request llm.Request, options llm.RequestOptions) (llm.Response, error) {
	client, _, _ := adapter.engine.liveModel()
	return client.Respond(ctx, request, options)
}

// discoverSkills loads skills from the workspace (.unreal/skills, and
// .harness/skills as upstream does) and from ~/.unreal-tui/skills. Workspace
// skills win on name clashes.
func discoverSkills(workspace, home string) ([]tool.Skill, []error) {
	var skills []tool.Skill
	var problems []error
	seen := make(map[string]bool)
	for _, directory := range []string{
		filepath.Join(workspace, projectDirName, "skills"),
		filepath.Join(workspace, ".harness", "skills"),
		filepath.Join(home, "skills"),
	} {
		if _, err := os.Stat(directory); err != nil {
			continue
		}
		found, errs := tool.DiscoverSkills(directory)
		problems = append(problems, errs...)
		for _, skill := range found {
			if seen[skill.Name] {
				continue
			}
			seen[skill.Name] = true
			skills = append(skills, skill)
		}
	}
	slices.SortFunc(skills, func(left, right tool.Skill) int { return cmp.Compare(left.Name, right.Name) })
	return skills, problems
}

func skillNames(skills []tool.Skill) []string {
	names := make([]string, 0, len(skills))
	for _, skill := range skills {
		names = append(names, skill.Name)
	}
	return names
}

func settingsInput(effort llm.ReasoningEffort) (inbox.Input, error) {
	payload, err := json.Marshal(inbox.ControlMessage{
		Mode:       inbox.UpdateSettings,
		Parameters: inbox.Settings{ReasoningEffort: effort},
	})
	if err != nil {
		return inbox.Input{}, fmt.Errorf("encode settings: %w", err)
	}
	return inbox.Input{ID: inbox.ID(uuid.New().String()), Kind: inbox.InputControl, Payload: payload}, nil
}

func recordedEffort(items []sessionstore.Item) llm.ReasoningEffort {
	var effort llm.ReasoningEffort
	for _, item := range items {
		input, ok := item.Data.(inbox.Input)
		if !ok || input.Kind != inbox.InputControl {
			continue
		}
		request, err := input.DecodeControlMessage()
		if err != nil || request.Mode != inbox.UpdateSettings {
			continue
		}
		effort = request.Parameters.(inbox.Settings).ReasoningEffort
	}
	return effort
}

func externalText(item sessionstore.Item) (string, bool) {
	input, ok := item.Data.(inbox.Input)
	if !ok || input.Kind != inbox.InputExternal {
		return "", false
	}
	var text string
	if err := json.Unmarshal(input.Payload, &text); err != nil {
		return "", false
	}
	return text, true
}

// eventQueue is an unbounded FIFO between the store observer, which runs
// synchronously inside coordinator writes, and the UI. Blocking the observer on
// the UI would deadlock when the UI itself triggers a write.
type eventQueue struct {
	mu     sync.Mutex
	items  []any
	signal chan struct{}
}

func newEventQueue() *eventQueue {
	return &eventQueue{signal: make(chan struct{}, 1)}
}

func (queue *eventQueue) push(event any) {
	queue.mu.Lock()
	queue.items = append(queue.items, event)
	queue.mu.Unlock()
	select {
	case queue.signal <- struct{}{}:
	default:
	}
}

func (queue *eventQueue) pump(ctx context.Context, emit func(any)) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-queue.signal:
		}
		queue.mu.Lock()
		items := queue.items
		queue.items = nil
		queue.mu.Unlock()
		for _, item := range items {
			emit(item)
		}
	}
}
