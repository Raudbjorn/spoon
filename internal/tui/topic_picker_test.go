package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/svnbjrn/spoon/internal/forge"
	"github.com/svnbjrn/spoon/internal/topics"
)

func topicFixture() []topics.Selection {
	return []topics.Selection{
		{TopicRepo: forge.TopicRepo{FullName: "alpha/one", Stars: 1000, ForkCount: 100, Description: "first"}, Score: 80},
		{TopicRepo: forge.TopicRepo{FullName: "beta/two", Stars: 500, ForkCount: 50, Description: "second"}, Score: 60},
	}
}

func TestHandleTopicResolved_ShowsPicker(t *testing.T) {
	m := newTestModel()
	mm, _ := m.handleTopicResolved(topicResolvedMsg{Topic: "tui", Selections: topicFixture()})
	model := mm.(*Model)
	if model.view != viewTopicPicker {
		t.Fatalf("view = %v, want viewTopicPicker", model.view)
	}
	out := model.viewTopicPicker()
	for _, want := range []string{"alpha/one", "beta/two", "Topic: tui"} {
		if !strings.Contains(out, want) {
			t.Errorf("picker render missing %q:\n%s", want, out)
		}
	}
}

func TestHandleTopicResolved_ErrorReturnsToInput(t *testing.T) {
	m := newTestModel()
	mm, _ := m.handleTopicResolved(topicResolvedMsg{Topic: "x", Err: errors.New("no repositories found")})
	model := mm.(*Model)
	if model.view != viewInput {
		t.Fatalf("view = %v, want viewInput on error", model.view)
	}
	if model.inputErr == "" {
		t.Error("inputErr should carry the resolution failure")
	}
}

func TestHandleTopicPickerKey_SelectStartsFetch(t *testing.T) {
	m := newTestModel()
	m.topicSelections = topicFixture()
	m.view = viewTopicPicker

	mm, _ := m.handleTopicPickerKey("j")
	model := mm.(*Model)
	if model.topicCursor != 1 {
		t.Fatalf("cursor = %d, want 1 after j", model.topicCursor)
	}
	mm, cmd := model.handleTopicPickerKey("enter")
	model = mm.(*Model)
	if model.input != "beta/two" {
		t.Fatalf("input = %q, want beta/two", model.input)
	}
	if cmd == nil {
		t.Fatal("enter must produce a fetch command")
	}
	if _, ok := cmd().(startFetchMsg); !ok {
		t.Fatal("enter must emit startFetchMsg")
	}
}
