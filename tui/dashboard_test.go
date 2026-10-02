package tui

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

func TestDashboardStaticGroupRendersDuringInitialization(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var tracker sync.WaitGroup
	model := newDashboardModel(ctx, cancel, DashboardConfig{
		Title: "Test", MaxConcurrency: 1,
		Groups: []DashboardGroup{{ID: "static", Title: "Static", Columns: 1, Widgets: []DashboardWidget{{ID: "one", Title: "One"}}}},
		Render: func(_, _ string, _ map[string]any, _ DashboardState, _ map[string]DashboardState) (string, error) {
			return "ready", nil
		},
	}, &tracker)
	if got := model.groups[0].content[0]; got != "ready" {
		t.Fatalf("content = %q, want ready", got)
	}
}

func TestDashboardQueuesRefreshRequestedWhileRunning(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var tracker sync.WaitGroup
	model := newDashboardModel(ctx, cancel, DashboardConfig{
		Title: "Test", MaxConcurrency: 1,
		Groups:  []DashboardGroup{{ID: "live", Title: "Live", Refresh: true, Widgets: []DashboardWidget{{ID: "one", Title: "One"}}}},
		Refresh: func(context.Context, string) (map[string]any, error) { return map[string]any{}, nil },
		Render: func(_, _ string, _ map[string]any, _ DashboardState, _ map[string]DashboardState) (string, error) {
			return "ready", nil
		},
	}, &tracker)
	model.queueRefresh(0)
	if !model.groups[0].pendingRefresh {
		t.Fatal("refresh requested while running was not marked pending")
	}
	commands := model.finishJob(dashboardJobResultMsg{job: dashboardJob{kind: dashboardRefreshJob, group: 0}, data: map[string]any{}, finished: time.Now()})
	if len(commands) != 1 || !model.groups[0].running {
		t.Fatalf("pending refresh was not started: commands=%d running=%v", len(commands), model.groups[0].running)
	}
	// The test does not execute the returned command, so balance the tracker increment.
	tracker.Done()
}

func TestSanitizeDashboardContent(t *testing.T) {
	got := sanitizeDashboardContent("\x1b[31mred\x1b[0m\x00\n ok ")
	if got != "red\n ok" || strings.Contains(got, "\x1b") {
		t.Fatalf("sanitizeDashboardContent() = %q", got)
	}
}

func TestDashboardGlobalExitKeysWorkInsideMenu(t *testing.T) {
	for _, test := range []struct {
		name     string
		key      tea.KeyPressMsg
		canceled bool
	}{
		{name: "quit", key: tea.KeyPressMsg{Code: 'q', Text: "q"}},
		{name: "cancel", key: tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}, canceled: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			var tracker sync.WaitGroup
			model := newDashboardModel(ctx, cancel, DashboardConfig{
				Title: "Test", Groups: []DashboardGroup{{ID: "static", Widgets: []DashboardWidget{{ID: "one"}}}},
				Render: func(_, _ string, _ map[string]any, _ DashboardState, _ map[string]DashboardState) (string, error) {
					return "ready", nil
				},
			}, &tracker)
			model.menu = true
			updated, command := model.Update(test.key)
			got := updated.(dashboardModel)
			if !got.done || got.canceled != test.canceled || command == nil {
				t.Fatalf("exit state = done %v canceled %v command nil %v", got.done, got.canceled, command == nil)
			}
		})
	}
}

func TestDashboardUsesWorkflowActionPalette(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var tracker sync.WaitGroup
	dashboard := newDashboardModel(ctx, cancel, DashboardConfig{
		Title: "Test", Groups: []DashboardGroup{{ID: "static", Widgets: []DashboardWidget{{
			ID: "one", Title: "One", Actions: []DashboardAction{{ID: "build", Label: "Build"}, {ID: "deploy", Label: "Deploy"}},
		}}}},
		Render: func(_, _ string, _ map[string]any, _ DashboardState, _ map[string]DashboardState) (string, error) {
			return "ready", nil
		},
	}, &tracker)
	updated, _ := dashboard.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	dashboard = updated.(dashboardModel)
	updated, _ = dashboard.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	dashboard = updated.(dashboardModel)

	workflowPicker := newSelectionModel("Workflows", []Option{{
		Label: "One", Actions: []SelectionAction{{Label: "Build"}, {Label: "Deploy"}},
	}})
	updated, _ = workflowPicker.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	workflowPicker = updated.(selectionModel)
	updated, _ = workflowPicker.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	workflowPicker = updated.(selectionModel)

	if got, want := dashboard.View().Content, workflowPicker.View().Content; got != want {
		t.Fatalf("dashboard action palette differs from workflow picker\n--- dashboard ---\n%s\n--- workflow ---\n%s", got, want)
	}
	if !strings.Contains(dashboard.View().Content, "Actions — One") || !strings.Contains(dashboard.View().Content, "> ") {
		t.Fatalf("dashboard action palette = %q", dashboard.View().Content)
	}
	if strings.Contains(dashboard.View().Content, "Build —") {
		t.Fatalf("dashboard action without a description has a dangling separator: %q", dashboard.View().Content)
	}
}

func newTwoGroupDashboard(t *testing.T, every time.Duration) dashboardModel {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	var tracker sync.WaitGroup
	widgets := []DashboardWidget{{ID: "one", Title: "One", Actions: []DashboardAction{{ID: "act", Label: "Act"}}}}
	return newDashboardModel(ctx, cancel, DashboardConfig{
		Title: "Test", MaxConcurrency: 4,
		Groups: []DashboardGroup{
			{ID: "a", Title: "A", Refresh: true, Every: every, Widgets: widgets},
			{ID: "b", Title: "B", Refresh: true, Every: every, Widgets: widgets},
		},
		Render: func(_, _ string, _ map[string]any, _ DashboardState, _ map[string]DashboardState) (string, error) {
			return "ready", nil
		},
	}, &tracker)
}

func TestDashboardActionBlockedByAnotherActionStartsAfterIt(t *testing.T) {
	model := newTwoGroupDashboard(t, 0)
	refreshDone := func(group int) dashboardJobResultMsg {
		return dashboardJobResultMsg{job: dashboardJob{kind: dashboardRefreshJob, group: group}, data: map[string]any{}, finished: time.Now()}
	}
	actionA := dashboardJob{kind: dashboardActionJob, group: 0}
	actionB := dashboardJob{kind: dashboardActionJob, group: 1}

	// Group a is still refreshing, so its action waits on the refresh.
	model.queueAction(actionA)
	model.startQueued()
	model.finishJob(refreshDone(1))
	model.queueAction(actionB)
	model.startQueued()
	if !model.actionRun {
		t.Fatal("action b did not start")
	}
	// Group a's refresh finishes while action b owns the action slot.
	model.finishJob(refreshDone(0))
	if len(model.queue) != 1 || model.queue[0].kind != dashboardActionJob || model.queue[0].group != 0 {
		t.Fatalf("blocked action was dropped from the queue: %#v", model.queue)
	}
	model.finishJob(dashboardJobResultMsg{job: actionB, data: map[string]any{}, finished: time.Now()})
	if got := model.groups[0].actions[0]["act"].Status; got != "running" {
		t.Fatalf("action a status = %q, want running", got)
	}
}

func TestDashboardIgnoresSupersededRefreshTicks(t *testing.T) {
	model := newTwoGroupDashboard(t, time.Hour)
	done := dashboardJobResultMsg{job: dashboardJob{kind: dashboardRefreshJob, group: 0}, data: map[string]any{}, finished: time.Now()}
	model.finishJob(done)
	stale := dashboardTickMsg{group: 0, generation: model.groups[0].tickGeneration}
	// A manual refresh completes before the scheduled tick fires.
	model.queueRefresh(0)
	model.startQueued()
	model.finishJob(done)
	updated, _ := model.Update(stale)
	if got := updated.(dashboardModel); got.groups[0].running || got.groups[0].queued {
		t.Fatal("superseded tick started another refresh chain")
	}
}
