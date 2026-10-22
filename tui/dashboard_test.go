package tui

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestDashboardStaticGroupRendersDuringInitialization(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var tracker sync.WaitGroup
	model := newDashboardModel(ctx, cancel, DashboardConfig{
		Title: "Test", MaxConcurrency: 1,
		Groups: []DashboardGroup{{ID: "static", Title: "Static", Columns: 1, Widgets: []DashboardWidget{{ID: "one", Title: "One"}}}},
		Render: func(_, _ string, _ DashboardWidgetDimensions, _ map[string]any, _ DashboardState, _ map[string]DashboardState) (string, error) {
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
		Render: func(_, _ string, _ DashboardWidgetDimensions, _ map[string]any, _ DashboardState, _ map[string]DashboardState) (string, error) {
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

func TestSanitizeDashboardContentPreservesFirstLineIndentation(t *testing.T) {
	got := sanitizeDashboardContent("\n  \n    ▄▇\n  ████  \n\n")
	if got != "    ▄▇\n  ████" {
		t.Fatalf("sanitizeDashboardContent() = %q", got)
	}
}

func TestDashboardCloseRequiresConfirmationAndEscapeDoesNotClose(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var tracker sync.WaitGroup
	model := newDashboardModel(ctx, cancel, DashboardConfig{
		Title: "Test", Groups: []DashboardGroup{{ID: "static", Widgets: []DashboardWidget{{ID: "one"}}}},
		Render: func(_, _ string, _ DashboardWidgetDimensions, _ map[string]any, _ DashboardState, _ map[string]DashboardState) (string, error) {
			return "ready", nil
		},
	}, &tracker)
	model.menu = true

	updated, command := model.Update(tea.KeyPressMsg{Code: 'q', Text: "q"})
	model = updated.(dashboardModel)
	if model.done || model.menu || model.confirm != dashboardCloseConfirmation || command != nil {
		t.Fatalf("close request state = done %v menu %v confirmation %v command nil %v", model.done, model.menu, model.confirm, command == nil)
	}
	if view := model.View().Content; !strings.Contains(view, "Close this dashboard? [y/N]") {
		t.Fatalf("close confirmation is not visible:\n%s", view)
	}

	updated, command = model.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	model = updated.(dashboardModel)
	if model.done || model.confirm != dashboardNoConfirmation || command != nil {
		t.Fatalf("dismissed close state = done %v confirmation %v command nil %v", model.done, model.confirm, command == nil)
	}
	updated, command = model.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	model = updated.(dashboardModel)
	if model.done || command != nil {
		t.Fatalf("escape closed dashboard: done %v command nil %v", model.done, command == nil)
	}

	updated, _ = model.Update(tea.KeyPressMsg{Code: 'q', Text: "q"})
	model = updated.(dashboardModel)
	updated, command = model.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	model = updated.(dashboardModel)
	if !model.done || model.canceled || command == nil {
		t.Fatalf("confirmed close state = done %v canceled %v command nil %v", model.done, model.canceled, command == nil)
	}
}

func TestDashboardEnterDeclinesConfirmation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup []tea.KeyPressMsg
		want  dashboardConfirmationKind
	}{
		{name: "action", setup: []tea.KeyPressMsg{{Code: tea.KeyEnter}, {Code: tea.KeyEnter}}, want: dashboardActionConfirmation},
		{name: "close", setup: []tea.KeyPressMsg{{Code: 'q', Text: "q"}}, want: dashboardCloseConfirmation},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var tracker sync.WaitGroup
			model := newDashboardModel(ctx, cancel, DashboardConfig{
				Title: "Test",
				Groups: []DashboardGroup{{
					ID: "ops", Title: "Ops", Columns: 1,
					Widgets: []DashboardWidget{{ID: "deploy", Title: "Deploy", Actions: []DashboardAction{{ID: "ship", Label: "Ship", Confirm: true}}}},
				}},
				Render: func(string, string, DashboardWidgetDimensions, map[string]any, DashboardState, map[string]DashboardState) (string, error) {
					return "ready", nil
				},
			}, &tracker)
			for _, key := range tc.setup {
				updated, _ := model.Update(key)
				model = updated.(dashboardModel)
			}
			if model.confirm != tc.want {
				t.Fatalf("confirmation = %v, want %v", model.confirm, tc.want)
			}
			updated, _ := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
			model = updated.(dashboardModel)
			if model.done || model.confirm != dashboardNoConfirmation || len(model.queue) != 0 {
				t.Fatalf("enter did not decline: done %v confirmation %v queued %d", model.done, model.confirm, len(model.queue))
			}
		})
	}
}

func TestDashboardCloseKeyKeepsPendingActionConfirmation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var tracker sync.WaitGroup
	model := newDashboardModel(ctx, cancel, DashboardConfig{
		Title: "Test",
		Groups: []DashboardGroup{{
			ID: "ops", Title: "Ops", Columns: 1,
			Widgets: []DashboardWidget{{ID: "deploy", Title: "Deploy", Actions: []DashboardAction{{ID: "ship", Label: "Ship", Confirm: true}}}},
		}},
		Render: func(string, string, DashboardWidgetDimensions, map[string]any, DashboardState, map[string]DashboardState) (string, error) {
			return "ready", nil
		},
	}, &tracker)
	for _, key := range []tea.KeyPressMsg{{Code: tea.KeyEnter}, {Code: tea.KeyEnter}, {Code: 'q', Text: "q"}} {
		updated, _ := model.Update(key)
		model = updated.(dashboardModel)
	}
	if model.done || model.confirm != dashboardActionConfirmation || model.confirmText != "Ship?" {
		t.Fatalf("pending action prompt replaced: done %v confirmation %v text %q", model.done, model.confirm, model.confirmText)
	}
}

func TestDashboardControlCCancelsInsideMenu(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	var tracker sync.WaitGroup
	model := newDashboardModel(ctx, cancel, DashboardConfig{
		Title: "Test", Groups: []DashboardGroup{{ID: "static", Widgets: []DashboardWidget{{ID: "one"}}}},
		Render: func(_, _ string, _ DashboardWidgetDimensions, _ map[string]any, _ DashboardState, _ map[string]DashboardState) (string, error) {
			return "ready", nil
		},
	}, &tracker)
	model.menu = true

	updated, command := model.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	got := updated.(dashboardModel)
	if !got.done || !got.canceled || command == nil {
		t.Fatalf("cancel state = done %v canceled %v command nil %v", got.done, got.canceled, command == nil)
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
		Render: func(_, _ string, _ DashboardWidgetDimensions, _ map[string]any, _ DashboardState, _ map[string]DashboardState) (string, error) {
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
	if !strings.Contains(dashboard.View().Content, "Actions — One") || !strings.Contains(dashboard.View().Content, "› ") {
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
		Render: func(_, _ string, _ DashboardWidgetDimensions, _ map[string]any, _ DashboardState, _ map[string]DashboardState) (string, error) {
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

func TestDashboardKeepsEveryActionQueuedBehindARefresh(t *testing.T) {
	model := newTwoGroupDashboard(t, 0)
	model.queueRefresh(0)
	model.startQueued()
	if !model.groups[0].running {
		t.Fatal("refresh did not start")
	}
	first := dashboardJob{kind: dashboardActionJob, group: 0, refreshGroups: []string{"first"}}
	second := dashboardJob{kind: dashboardActionJob, group: 0, refreshGroups: []string{"second"}}
	model.queueAction(first)
	model.queueAction(second)
	model.finishJob(dashboardJobResultMsg{job: dashboardJob{kind: dashboardRefreshJob, group: 0}, data: map[string]any{}, finished: time.Now()})
	// The first action now owns the action slot; the second must still be waiting for it.
	if !model.actionRun {
		t.Fatal("first action did not start after the refresh")
	}
	if len(model.queue) != 1 || model.queue[0].kind != dashboardActionJob || model.queue[0].refreshGroups[0] != "second" {
		t.Fatalf("queue = %#v, want the second action still queued", model.queue)
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

func TestDashboardDistributesResponsiveWidgetDimensions(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var tracker sync.WaitGroup
	var dimensions map[string]DashboardWidgetDimensions
	var confirmationSize DashboardWidgetDimensions
	model := newDashboardModel(ctx, cancel, DashboardConfig{
		Title: "Test",
		Groups: []DashboardGroup{{
			ID: "metrics", Title: "Metrics", Columns: 3,
			Widgets: []DashboardWidget{
				{ID: "one", Title: "One"},
				{ID: "two", Title: "Two", Actions: []DashboardAction{{ID: "inspect", Label: "Inspect"}}},
				{ID: "three", Title: "Three"},
			},
		}},
		Render: func(_, widgetID string, size DashboardWidgetDimensions, _ map[string]any, _ DashboardState, _ map[string]DashboardState) (string, error) {
			if dimensions == nil {
				dimensions = make(map[string]DashboardWidgetDimensions)
			}
			dimensions[widgetID] = size
			return "ready", nil
		},
		Confirmation: func(_, _, _ string, size DashboardWidgetDimensions, _ map[string]any, _ DashboardState, _ map[string]DashboardState) (string, error) {
			confirmationSize = size
			return "Confirm?", nil
		},
	}, &tracker)

	updated, _ := model.Update(tea.WindowSizeMsg{Width: 90, Height: 20})
	model = updated.(dashboardModel)
	if got, want := dimensions["one"].Width, 26; got != want {
		t.Fatalf("first widget width = %d, want %d", got, want)
	}
	if got, want := dimensions["three"].Width, 25; got != want {
		t.Fatalf("last widget width = %d, want %d", got, want)
	}
	if got, want := dimensions["one"].Height, dimensions["two"].Height+1; got != want {
		t.Fatalf("body heights without/with action = %d/%d, want one reserved action row", dimensions["one"].Height, dimensions["two"].Height)
	}
	if got, want := lipgloss.Height(model.renderPage()), model.pageSize(); got != want {
		t.Fatalf("fitted page height = %d, want viewport height %d", got, want)
	}
	if got, want := lipgloss.Width(model.renderWidgetGrid(0)), 90; got != want {
		t.Fatalf("widget grid width = %d, want %d", got, want)
	}
	if _, err := model.confirmation(dashboardJob{group: 0, widget: 1, action: 0}); err != nil {
		t.Fatal(err)
	}
	if confirmationSize != dimensions["two"] {
		t.Fatalf("confirmation dimensions = %#v, want %#v", confirmationSize, dimensions["two"])
	}
}

func TestDashboardResizeRerendersWithoutRefreshing(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var tracker sync.WaitGroup
	renders := 0
	refreshes := 0
	var sizes []DashboardWidgetDimensions
	model := newDashboardModel(ctx, cancel, DashboardConfig{
		Title:  "Test",
		Groups: []DashboardGroup{{ID: "static", Title: "Static", Columns: 1, Widgets: []DashboardWidget{{ID: "one", Title: "One"}}}},
		Refresh: func(context.Context, string) (map[string]any, error) {
			refreshes++
			return map[string]any{}, nil
		},
		Render: func(_, _ string, size DashboardWidgetDimensions, _ map[string]any, _ DashboardState, _ map[string]DashboardState) (string, error) {
			renders++
			sizes = append(sizes, size)
			return "ready", nil
		},
	}, &tracker)
	initialRenders := renders
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 60, Height: 16})
	if _, ok := updated.(dashboardModel); !ok {
		t.Fatal("resize returned a different model type")
	}
	if renders <= initialRenders {
		t.Fatal("resize did not rerender the widget template")
	}
	if refreshes != 0 {
		t.Fatalf("resize invoked refresh %d times", refreshes)
	}
	if sizes[len(sizes)-1] == sizes[0] {
		t.Fatalf("dimensions did not change on resize: %#v", sizes)
	}
}

func TestDashboardShortViewportKeepsPageScrolling(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var tracker sync.WaitGroup
	model := newDashboardModel(ctx, cancel, DashboardConfig{
		Title: "Test",
		Groups: []DashboardGroup{{
			ID: "static", Title: "Static", Columns: 1,
			Widgets: []DashboardWidget{{ID: "one", Title: "One"}, {ID: "two", Title: "Two"}, {ID: "three", Title: "Three"}},
		}},
		Render: func(_, _ string, _ DashboardWidgetDimensions, _ map[string]any, _ DashboardState, _ map[string]DashboardState) (string, error) {
			return "ready", nil
		},
	}, &tracker)
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 40, Height: 8})
	model = updated.(dashboardModel)
	for _, widget := range model.layout.groups[0].widgets {
		if widget.Height != 1 {
			t.Fatalf("minimum body height = %d, want 1", widget.Height)
		}
	}
	if model.maxOffset() == 0 {
		t.Fatal("short viewport did not retain page scrolling")
	}
}

func TestFitDashboardContentMarksOverflow(t *testing.T) {
	got := fitDashboardContent("one two three four", 7, 2)
	if width, height := lipgloss.Size(got); width != 7 || height != 2 {
		t.Fatalf("content size = %dx%d, want 7x2", width, height)
	}
	lines := strings.Split(got, "\n")
	if !strings.HasSuffix(lines[len(lines)-1], "…") {
		t.Fatalf("overflow content = %q, want ellipsis on final line", got)
	}
}

func TestDashboardPageSizeReservesWrappedHelp(t *testing.T) {
	model := dashboardModel{width: 18, height: 12}
	want := model.height - lipgloss.Height(model.dashboardHelpText()) - 1
	if got := model.pageSize(); got != want {
		t.Fatalf("page size = %d, want %d", got, want)
	}
}

func TestDashboardHelpUsesCompactKeyLegendAndExpands(t *testing.T) {
	model := dashboardModel{width: 200, height: 20}
	shortHelp := model.dashboardHelpText()
	for _, want := range []string{"↑/k up", "↓/j down", "tab focus", "enter/a actions", "r refresh", "q close", cancelHelp, "? more"} {
		if !strings.Contains(strings.ReplaceAll(ansi.Strip(shortHelp), "\n", ""), want) {
			t.Fatalf("short help = %q, want %q", shortHelp, want)
		}
	}
	if strings.Contains(shortHelp, "pgup/pgdown page") {
		t.Fatalf("short help = %q, unexpectedly contains full navigation help", shortHelp)
	}

	updated, _ := model.Update(tea.KeyPressMsg{Code: '?'})
	model = updated.(dashboardModel)
	fullHelp := model.dashboardHelpText()
	for _, want := range []string{"pgup/pgdown page", "home/end bounds", "tab/shift+tab focus", "q close", "? less"} {
		if !strings.Contains(strings.ReplaceAll(ansi.Strip(fullHelp), "\n", ""), want) {
			t.Fatalf("full help = %q, want %q", fullHelp, want)
		}
	}
}

func TestDashboardStateTransitionsReallocateViewportHeight(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var tracker sync.WaitGroup
	latest := make(map[string]DashboardWidgetDimensions)
	model := newDashboardModel(ctx, cancel, DashboardConfig{
		Title: "Test", MaxConcurrency: 1,
		Groups: []DashboardGroup{
			{ID: "static", Title: "Static", Columns: 1, Widgets: []DashboardWidget{{ID: "one", Title: "One"}}},
			{ID: "live", Title: "Live", Columns: 1, Refresh: true, Widgets: []DashboardWidget{{ID: "two", Title: "Two"}}},
		},
		Refresh: func(context.Context, string) (map[string]any, error) { return map[string]any{"value": 1}, nil },
		Render: func(groupID, _ string, size DashboardWidgetDimensions, _ map[string]any, _ DashboardState, _ map[string]DashboardState) (string, error) {
			latest[groupID] = size
			return "ready", nil
		},
	}, &tracker)
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 60, Height: 24})
	model = updated.(dashboardModel)
	staticWhileLoading := latest["static"].Height

	model.finishJob(dashboardJobResultMsg{
		job: dashboardJob{kind: dashboardRefreshJob, group: 1}, data: map[string]any{"value": 1}, finished: time.Now(),
	})
	model.reflow()
	if latest["live"].Height < 1 {
		t.Fatalf("live widget dimensions = %#v", latest["live"])
	}
	if latest["static"].Height >= staticWhileLoading {
		t.Fatalf("static height after live group appeared = %d, want less than %d", latest["static"].Height, staticWhileLoading)
	}

	staticWithLive := latest["static"].Height
	model.groups[1].Status = "failed"
	model.groups[1].Error = "boom"
	model.reflow()
	if latest["static"].Height <= staticWithLive {
		t.Fatalf("static height after live failure = %d, want more than %d", latest["static"].Height, staticWithLive)
	}
}

func TestDashboardConfirmationPromptFitsViewport(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var tracker sync.WaitGroup
	model := newDashboardModel(ctx, cancel, DashboardConfig{
		Title: "Test",
		Groups: []DashboardGroup{{
			ID: "ops", Title: "Ops", Columns: 1,
			Widgets: []DashboardWidget{{ID: "deploy", Title: "Deploy", Actions: []DashboardAction{{ID: "ship", Label: "Ship", Confirm: true}}}},
		}},
		Render: func(string, string, DashboardWidgetDimensions, map[string]any, DashboardState, map[string]DashboardState) (string, error) {
			return "ready", nil
		},
	}, &tracker)
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 120, Height: 20})
	model = updated.(dashboardModel)
	for _, key := range []tea.KeyPressMsg{{Code: tea.KeyEnter}, {Code: tea.KeyEnter}} {
		updated, _ = model.Update(key)
		model = updated.(dashboardModel)
	}
	if model.confirm != dashboardActionConfirmation {
		t.Fatal("expected confirmation prompt")
	}
	if got, want := lipgloss.Height(model.renderPage()), model.pageSize(); got != want {
		t.Fatalf("page height with confirmation = %d, want viewport height %d", got, want)
	}
	if view := model.View().Content; !strings.Contains(view, "[y/N]") {
		t.Fatalf("confirmation prompt is not visible:\n%s", view)
	}

	updated, _ = model.Update(tea.KeyPressMsg{Code: 'n', Text: "n"})
	model = updated.(dashboardModel)
	if got, want := lipgloss.Height(model.renderPage()), model.pageSize(); got != want {
		t.Fatalf("page height after cancel = %d, want viewport height %d", got, want)
	}
}
