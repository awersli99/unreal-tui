package config

import (
	"cmp"
	"os"
	"path/filepath"
	"slices"

	"github.com/unreallabsai/unreal-agent/harness/tool"
)

// DiscoverSkills loads skills from the workspace (.unreal/skills, and
// .harness/skills as upstream does) and from ~/.unreal-tui/skills. Workspace
// skills win on name clashes.
func DiscoverSkills(workspace, home string) ([]tool.Skill, []error) {
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

func SkillNames(skills []tool.Skill) []string {
	names := make([]string, 0, len(skills))
	for _, skill := range skills {
		names = append(names, skill.Name)
	}
	return names
}
