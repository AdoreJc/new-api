package contentmoderation

import (
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/content_moderation_setting"

	goahocorasick "github.com/anknown/ahocorasick"
)

// keywordIndex holds one Aho-Corasick automaton per stored keyword list. It
// is built once per change of the stored lists and swapped atomically.
type keywordIndex struct {
	version  string
	machines map[string]*goahocorasick.Machine
}

// matchKeyword returns the first of the given lists that has a keyword in
// the text. A name without a stored list matches nothing.
func (s *Service) matchKeyword(text string, lists []string) (string, bool) {
	if text == "" {
		return "", false
	}
	index := s.keywords.Load()
	if index == nil {
		return "", false
	}
	var lowered []rune
	for _, name := range lists {
		machine := index.machines[name]
		if machine == nil {
			continue
		}
		if lowered == nil {
			lowered = []rune(strings.ToLower(text))
		}
		if len(machine.MultiPatternSearch(lowered, true)) > 0 {
			return name, true
		}
	}
	return "", false
}

// reloadKeywords rebuilds the automatons when the stored lists changed since
// the last build. It is cheap when nothing changed: one summary query.
func (s *Service) reloadKeywords(force bool) error {
	// One build at a time: a reload that read the lists earlier can never
	// store its result over a newer one.
	s.keywordMu.Lock()
	defer s.keywordMu.Unlock()
	version, err := model.ContentModerationKeywordListsVersion()
	if err != nil {
		return err
	}
	if current := s.keywords.Load(); !force && current != nil && current.version == version {
		return nil
	}
	lists, err := model.ListContentModerationKeywordLists()
	if err != nil {
		return err
	}
	machines := make(map[string]*goahocorasick.Machine, len(lists))
	for _, list := range lists {
		words, err := content_moderation_setting.NormalizeKeywords(string(list.Words))
		if err != nil || len(words) == 0 {
			continue
		}
		if machine := service.InitAc(words); machine != nil {
			machines[list.Name] = machine
		}
	}
	s.keywords.Store(&keywordIndex{version: version, machines: machines})
	return nil
}

// ReloadKeywordLists rebuilds this node's keyword automatons after an
// administrator changed a list; other nodes pick the change up within a
// minute.
func ReloadKeywordLists() {
	if err := defaultService.reloadKeywords(true); err != nil {
		common.SysError(common.LogText("failed to load content moderation keyword lists: %s", err.Error()))
	}
}
