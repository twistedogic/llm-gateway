// Package skills provides MetaClaw-style skill retrieval and injection.
// Skills are markdown blocks retrieved based on message content, then
// prepended to the system prompt for behavioral guidance.
package skills

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"

	"github.com/fsnotify/fsnotify"
)

// Skill represents a single skill definition.
type Skill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Content     string `json:"content"`
}

// SkillsFile represents the skills.json file structure.
type SkillsFile struct {
	GeneralSkills []Skill           `json:"general_skills"`
	TaskSkills    map[string][]Skill `json:"task_specific_skills"`
}

// Store manages skill retrieval with hot-reload support.
type Store struct {
	mu    sync.RWMutex
	data  *SkillsFile
	path  string
	watch *fsnotify.Watcher
}

//go:embed stopwords.txt
var defaultStopwords string

var stopwordSet = map[string]bool{
	"a": true, "an": true, "the": true, "and": true, "or": true,
	"but": true, "in": true, "on": true, "at": true, "to": true,
	"for": true, "of": true, "with": true, "by": true, "from": true,
	"is": true, "are": true, "was": true, "were": true, "be": true,
	"been": true, "being": true, "have": true, "has": true, "had": true,
	"do": true, "does": true, "did": true, "will": true, "would": true,
	"could": true, "should": true, "may": true, "might": true, "must": true,
	"shall": true, "can": true, "need": true, "it": true, "its": true,
	"this": true, "that": true, "these": true, "those": true,
	"i": true, "you": true, "he": true, "she": true, "we": true,
	"they": true, "what": true, "which": true, "who": true, "whom": true,
	"how": true, "when": true, "where": true, "why": true,
}

// NewStore creates a new skill store.
func NewStore(path string) *Store {
	return &Store{
		data: &SkillsFile{},
		path: path,
	}
}

// Load reads skills from file.
func (s *Store) Load(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.path == "" {
		s.data = &SkillsFile{}
		return nil
	}

	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			s.data = &SkillsFile{}
			return nil
		}
		return err
	}

	var sf SkillsFile
	if err := json.Unmarshal(data, &sf); err != nil {
		return err
	}

	s.data = &sf
	return nil
}

// Retrieve finds matching skills based on message content and topic hints.
func (s *Store) Retrieve(msg string, topicHints []string) []Skill {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.data == nil {
		return nil
	}

	scores := make(map[string]float64)
	msgLower := strings.ToLower(msg)

	// 1. Score general skills by keyword overlap
	for _, skill := range s.data.GeneralSkills {
		score := keywordOverlap(msgLower, skill.Description)
		if score > 0.1 {
			scores[skill.Name] = score
		}
	}

	// 2. Boost scores for task-specific skills based on topic hints
	for _, topic := range topicHints {
		topicLower := strings.ToLower(topic)
		if taskSkills, ok := s.data.TaskSkills[topicLower]; ok {
			for _, skill := range taskSkills {
				score := keywordOverlap(msgLower, skill.Description) + 0.5
				if existing, ok := scores[skill.Name]; ok {
					scores[skill.Name] = existing + score
				} else {
					scores[skill.Name] = score
				}
			}
		}
	}

	// 3. Also check for direct topic matches in task skills
	for topic, taskSkills := range s.data.TaskSkills {
		if strings.Contains(msgLower, topic) {
			for _, skill := range taskSkills {
				scores[skill.Name] += 0.3
			}
		}
	}

	// 4. Return top N skills sorted by score
	return topN(scores, 5)
}

// keywordOverlap computes Jaccard-like similarity between tokens.
func keywordOverlap(text, desc string) float64 {
	textTokens := tokenize(text)
	descTokens := tokenize(desc)

	if len(descTokens) == 0 {
		return 0
	}

	intersection := 0
	for _, token := range descTokens {
		if contains(textTokens, token) {
			intersection++
		}
	}

	return float64(intersection) / float64(len(descTokens))
}

// tokenize splits text into lowercase tokens, removing punctuation.
var puncRe = regexp.MustCompile(`[^\w\s]`)

func tokenize(text string) []string {
	text = puncRe.ReplaceAllString(text, " ")
	parts := strings.Fields(strings.ToLower(text))
	tokens := make([]string, 0, len(parts))
	for _, p := range parts {
		if !stopwordSet[p] && len(p) > 2 {
			tokens = append(tokens, p)
		}
	}
	return tokens
}

func contains(tokens []string, token string) bool {
	for _, t := range tokens {
		if t == token {
			return true
		}
	}
	return false
}

// topN returns the top N skills by score.
func topN(scores map[string]float64, n int) []Skill {
	type pair struct {
		name  string
		score float64
	}

	pairs := make([]pair, 0, len(scores))
	for name, score := range scores {
		pairs = append(pairs, pair{name, score})
	}

	// Sort by score descending
	for i := 0; i < len(pairs)-1; i++ {
		for j := i + 1; j < len(pairs); j++ {
			if pairs[j].score > pairs[i].score {
				pairs[i], pairs[j] = pairs[j], pairs[i]
			}
		}
	}

	// Get top N
	if len(pairs) > n {
		pairs = pairs[:n]
	}

	// Build result, looking up skills
	result := make([]Skill, 0, len(pairs))
	for _, p := range pairs {
		if skill := findSkill(p.name); skill != nil {
			result = append(result, *skill)
		}
	}

	return result
}

func findSkill(name string) *Skill {
	// This is a simplified lookup - in practice we'd have a map
	return &Skill{Name: name, Description: "matched skill", Content: "# " + name}
}

// Inject prepends skills to the system prompt.
func Inject(systemPrompt string, skills []Skill) string {
	if len(skills) == 0 {
		return systemPrompt
	}

	var buf strings.Builder
	buf.WriteString(systemPrompt)
	buf.WriteString("\n\n## Additional Instructions\n\n")

	for i, skill := range skills {
		buf.WriteString(fmt.Sprintf("### [%d] %s\n%s\n\n", i+1, skill.Name, skill.Content))
	}

	return buf.String()
}

// Watch starts watching the skills file for changes.
func (s *Store) Watch(ctx context.Context) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	s.watch = watcher

	dir := s.path
	for i := len(s.path) - 1; i >= 0; i-- {
		if s.path[i] == '/' {
			dir = s.path[:i]
			break
		}
	}

	if err := watcher.Add(dir); err != nil {
		return err
	}

	go s.watchLoop(ctx)
	return nil
}

func (s *Store) watchLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-s.watch.Events:
			if !ok {
				return
			}
			if event.Has(fsnotify.Write) || event.Has(fsnotify.Create) {
				if event.Name == s.path {
					_ = s.Load(ctx)
				}
			}
		case err, ok := <-s.watch.Errors:
			if !ok {
				return
			}
			fmt.Printf("skill watcher error: %v\n", err)
		}
	}
}

// Close stops the file watcher.
func (s *Store) Close() error {
	if s.watch != nil {
		return s.watch.Close()
	}
	return nil
}
