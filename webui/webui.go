// Package webui serves one loopback-only workflow form session.
package webui

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"maps"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/up2jj/wuko/correlation"
	"github.com/up2jj/wuko/form"
)

// Progress is the browser-safe projection of one execution event.
type Progress struct {
	InvocationID    correlation.InvocationID `json:"invocation_id,omitempty"`
	RunID           correlation.RunID        `json:"run_id,omitempty"`
	ParentRunID     correlation.RunID        `json:"parent_run_id,omitempty"`
	ParentStepRunID correlation.StepRunID    `json:"parent_step_run_id,omitempty"`
	StepRunID       correlation.StepRunID    `json:"step_run_id,omitempty"`
	Sequence        correlation.Sequence     `json:"sequence,omitempty"`
	Stage           string                   `json:"stage"`
	Kind            string                   `json:"kind"`
	Status          string                   `json:"status"`
	StepID          string                   `json:"step_id,omitempty"`
	StepType        string                   `json:"step_type,omitempty"`
	Index           int                      `json:"index,omitempty"`
	Total           int                      `json:"total,omitempty"`
	Attempt         int                      `json:"attempt,omitempty"`
	Duration        string                   `json:"duration,omitempty"`
}

// Summary is the deliberately small result surface exposed to browser templates.
type Summary struct {
	Total, Succeeded, Failed, Skipped, Canceled, TimedOut, Attempts, Retries int
}

// Result is the final browser-safe workflow outcome.
type Result struct {
	WorkflowName        string
	WorkflowDescription string
	Status              string
	Outputs             map[string]any
	Stats               Summary
	Duration            time.Duration
	Err                 error
}

// LoadFunc produces form-only data before fields are displayed.
// It must return when its context is canceled.
type LoadFunc func(context.Context, func(Progress)) (map[string]any, error)

// RunFunc executes the main workflow after a valid submission.
// It must return when its context is canceled.
type RunFunc func(context.Context, map[string]any, func(Progress)) Result

// Options configures process-owned UI capabilities.
type Options struct {
	OpenURL func(string) error
	NoOpen  bool
	Output  io.Writer
}

type phase string

const (
	phaseLoading phase = "loading"
	phaseReady   phase = "ready"
	phaseRunning phase = "running"
	phaseDone    phase = "done"
)

type session struct {
	mu         sync.Mutex
	definition *form.Definition
	vars       map[string]any
	fields     []form.ResolvedField
	phase      phase
	progress   []Progress
	fieldErrs  map[string]string
	result     Result
	csrf       string
	host       string
	changed    chan struct{}
	completed  chan struct{}
	delivered  chan struct{}
	complete   sync.Once
	deliver    sync.Once
	workers    sync.WaitGroup
	stopping   bool
	run        RunFunc
	ctx        context.Context
}

// Run serves one form, performs optional loading, executes once, and returns the workflow error.
func Run(ctx context.Context, definition *form.Definition, vars map[string]any, load LoadFunc, execute RunFunc, options Options) error {
	token, err := randomToken()
	if err != nil {
		return err
	}
	csrf, err := randomToken()
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("listening for workflow UI: %w", err)
	}
	defer listener.Close()

	initialPhase := phaseReady
	if load != nil {
		initialPhase = phaseLoading
	}
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	s := &session{
		definition: definition, vars: maps.Clone(vars), phase: initialPhase, csrf: csrf,
		host: listener.Addr().String(), changed: make(chan struct{}), completed: make(chan struct{}), delivered: make(chan struct{}),
		run: execute, ctx: runCtx,
	}
	if load == nil {
		fields, err := definition.Resolve(vars, nil)
		if err != nil {
			return err
		}
		s.fields = fields
	}

	prefix := "/" + token
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+prefix+"/", s.page)
	mux.HandleFunc("GET "+prefix+"/app.js", appScript)
	mux.HandleFunc("POST "+prefix+"/submit", s.submit)
	mux.HandleFunc("GET "+prefix+"/events", s.events)
	server := &http.Server{
		Handler:           securityHeaders(mux),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      0,
		IdleTimeout:       60 * time.Second,
	}
	serverDone := make(chan error, 1)
	go func() {
		err := server.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		serverDone <- err
	}()

	uiURL := "http://" + listener.Addr().String() + prefix + "/"
	if options.Output != nil {
		fmt.Fprintf(options.Output, "Workflow UI: %s\n", uiURL)
	}
	if !options.NoOpen && options.OpenURL != nil {
		if err := options.OpenURL(uiURL); err != nil && options.Output != nil {
			fmt.Fprintf(options.Output, "Opening browser failed: %v\n", err)
		}
	}

	if load != nil {
		s.workers.Go(func() {
			data, err := load(runCtx, s.publish)
			s.mu.Lock()
			defer s.mu.Unlock()
			if err != nil {
				s.phase = phaseDone
				s.result = Result{Status: "failed", Err: fmt.Errorf("loading form data: %w", err)}
				s.completeLocked()
				return
			}
			fields, err := definition.Resolve(vars, data)
			if err != nil {
				s.phase = phaseDone
				s.result = Result{Status: "failed", Err: err}
				s.completeLocked()
				return
			}
			s.fields = fields
			s.phase = phaseReady
			s.notifyLocked()
		})
	}

	var runErr error
	serverExited := false
	select {
	case <-ctx.Done():
		runErr = ctx.Err()
	case err := <-serverDone:
		serverExited = true
		if err != nil {
			runErr = fmt.Errorf("serving workflow UI: %w", err)
		}
	case <-s.completed:
		select {
		case <-ctx.Done():
			runErr = ctx.Err()
		case <-s.delivered:
		case <-time.After(30 * time.Second):
		}
		s.mu.Lock()
		if runErr == nil {
			runErr = s.result.Err
		}
		s.mu.Unlock()
	}
	s.mu.Lock()
	s.stopping = true
	s.mu.Unlock()
	cancelRun()
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	shutdownErr := server.Shutdown(shutdownCtx)
	if shutdownErr != nil {
		shutdownErr = errors.Join(shutdownErr, server.Close())
	}
	if !serverExited {
		if err := <-serverDone; err != nil {
			runErr = errors.Join(runErr, fmt.Errorf("serving workflow UI: %w", err))
		}
	}
	s.workers.Wait()
	return errors.Join(runErr, shutdownErr)
}

func (s *session) publish(event Progress) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.progress) >= 200 {
		copy(s.progress, s.progress[len(s.progress)-199:])
		s.progress = s.progress[:199]
	}
	s.progress = append(s.progress, event)
	s.notifyLocked()
}

func (s *session) notifyLocked() {
	close(s.changed)
	s.changed = make(chan struct{})
}

func (s *session) completeLocked() {
	s.notifyLocked()
	s.complete.Do(func() { close(s.completed) })
}

func (s *session) validRequest(response http.ResponseWriter, request *http.Request) bool {
	if request.Host != s.host {
		http.Error(response, "invalid host", http.StatusForbidden)
		return false
	}
	return true
}

func (s *session) page(response http.ResponseWriter, request *http.Request) {
	if !s.validRequest(response, request) {
		return
	}
	s.mu.Lock()
	data := s.pageDataLocked()
	done := s.phase == phaseDone
	s.mu.Unlock()
	response.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := pageTemplate.Execute(response, data); err != nil {
		http.Error(response, "rendering page", http.StatusInternalServerError)
		return
	}
	if done {
		s.deliver.Do(func() { close(s.delivered) })
	}
}

func (s *session) submit(response http.ResponseWriter, request *http.Request) {
	if !s.validRequest(response, request) {
		return
	}
	if err := request.ParseForm(); err != nil {
		http.Error(response, "invalid form submission", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	if s.stopping || s.phase != phaseReady || request.PostForm.Get("csrf") != s.csrf {
		s.mu.Unlock()
		http.Error(response, "invalid or expired submission", http.StatusForbidden)
		return
	}
	values, fieldErrs := form.Submit(s.fields, request.PostForm)
	if len(fieldErrs) > 0 {
		s.fieldErrs = fieldErrs
		data := s.pageDataLocked()
		s.mu.Unlock()
		response.Header().Set("Content-Type", "text/html; charset=utf-8")
		response.WriteHeader(http.StatusUnprocessableEntity)
		_ = pageTemplate.Execute(response, data)
		return
	}
	s.phase = phaseRunning
	s.fieldErrs = nil
	s.progress = nil
	s.notifyLocked()

	s.workers.Go(func() {
		result := s.run(s.ctx, values, s.publish)
		if result.Status == "" {
			if result.Err == nil {
				result.Status = "succeeded"
			} else {
				result.Status = "failed"
			}
		}
		s.mu.Lock()
		s.result = result
		s.phase = phaseDone
		s.completeLocked()
		s.mu.Unlock()
	})
	s.mu.Unlock()
	http.Redirect(response, request, "./", http.StatusSeeOther)
}

func (s *session) events(response http.ResponseWriter, request *http.Request) {
	if !s.validRequest(response, request) {
		return
	}
	flusher, ok := response.(http.Flusher)
	if !ok {
		http.Error(response, "streaming unavailable", http.StatusInternalServerError)
		return
	}
	response.Header().Set("Content-Type", "text/event-stream")
	response.Header().Set("Cache-Control", "no-cache")
	for {
		s.mu.Lock()
		snapshot := struct {
			Phase    phase      `json:"phase"`
			Progress []Progress `json:"progress"`
		}{s.phase, append([]Progress(nil), s.progress...)}
		changed := s.changed
		s.mu.Unlock()
		encoded, _ := json.Marshal(snapshot)
		fmt.Fprintf(response, "data: %s\n\n", encoded)
		flusher.Flush()
		if snapshot.Phase == phaseReady || snapshot.Phase == phaseDone {
			return
		}
		select {
		case <-request.Context().Done():
			return
		case <-changed:
		}
	}
}

type pageData struct {
	Title, Description string
	Phase              phase
	Fields             []form.ResolvedField
	Errors             map[string]string
	CSRF               string
	ResultTitle        string
	ResultHTML         template.HTML
	Result             Result
}

func (s *session) pageDataLocked() pageData {
	data := pageData{
		Title: s.definition.Title, Description: s.definition.Description, Phase: s.phase,
		Fields: s.fields, Errors: maps.Clone(s.fieldErrs), CSRF: s.csrf, Result: s.result,
	}
	if s.phase == phaseDone {
		data.ResultTitle, data.ResultHTML = renderResult(s.definition, s.result)
	}
	return data
}

func renderResult(definition *form.Definition, result Result) (string, template.HTML) {
	view := definition.Result.Success
	title := "Workflow complete"
	if result.Err != nil {
		view = definition.Result.Failure
		title = "Workflow failed"
	}
	if view.Title != "" {
		title = view.Title
	}
	if strings.TrimSpace(view.Template) == "" {
		if result.Err != nil {
			return title, template.HTML("<p>" + template.HTMLEscapeString(result.Err.Error()) + "</p>")
		}
		return title, template.HTML("<p>The workflow completed successfully.</p>")
	}
	tmpl, err := template.New("result").Option("missingkey=error").Parse(view.Template)
	if err != nil {
		return title, template.HTML("<p>Custom result template is invalid.</p>")
	}
	data := map[string]any{
		"workflow": map[string]any{"name": result.WorkflowName, "description": result.WorkflowDescription},
		"status":   result.Status, "outputs": result.Outputs, "stats": result.Stats,
		"duration": result.Duration.String(), "error": errorString(result.Err),
	}
	var output bytes.Buffer
	if err := tmpl.Execute(&output, data); err != nil {
		return title, template.HTML("<p>Custom result template could not be rendered.</p>")
	}
	return title, template.HTML(output.String())
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func randomToken() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generating workflow UI token: %w", err)
	}
	return hex.EncodeToString(value), nil
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("X-Content-Type-Options", "nosniff")
		response.Header().Set("Referrer-Policy", "no-referrer")
		response.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'unsafe-inline'; script-src 'self'; connect-src 'self'; frame-ancestors 'none'")
		next.ServeHTTP(response, request)
	})
}

func appScript(response http.ResponseWriter, _ *http.Request) {
	response.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	_, _ = io.WriteString(response, appJS)
}

var pageTemplate = template.Must(template.New("page").Funcs(template.FuncMap{
	"fieldName":  func(index int) string { return "field_" + fmt.Sprint(index) },
	"fieldError": func(errors map[string]string, variable string) string { return errors[variable] },
	"stringValue": func(field form.ResolvedField) string {
		if field.Secret || field.Value == nil {
			return ""
		}
		if field.Type == form.TypeArray || field.Type == form.TypeObject {
			encoded, _ := json.MarshalIndent(field.Value, "", "  ")
			return string(encoded)
		}
		return fmt.Sprint(field.Value)
	},
	"checked": func(field form.ResolvedField) bool { value, _ := field.Value.(bool); return value },
	"selected": func(field form.ResolvedField, choice form.Choice) bool {
		if field.Type == form.TypeArray {
			values, _ := field.Value.([]any)
			for _, value := range values {
				if fmt.Sprint(value) == fmt.Sprint(choice.Value) {
					return true
				}
			}
			return false
		}
		return fmt.Sprint(field.Value) == fmt.Sprint(choice.Value)
	},
}).Parse(pageHTML))

const pageHTML = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>{{.Title}} · Wuko</title><style>
:root{color-scheme:light dark;--bg:#f5f7fb;--panel:#fff;--text:#18202b;--muted:#657184;--accent:#6d4aff;--border:#dce1ea;--danger:#c93636} @media(prefers-color-scheme:dark){:root{--bg:#11141a;--panel:#1a1f28;--text:#eef2f7;--muted:#9ba7b8;--accent:#9c88ff;--border:#303846;--danger:#ff7777}}
*{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--text);font:16px/1.5 system-ui,sans-serif}.shell{max-width:760px;margin:7vh auto;padding:0 20px}.panel{background:var(--panel);border:1px solid var(--border);border-radius:16px;padding:28px;box-shadow:0 12px 35px #0001}h1{margin:0 0 6px;font-size:1.8rem}p{margin:8px 0}.muted,.description{color:var(--muted)}.field{margin:24px 0}.field>label,.legend{display:block;font-weight:650;margin-bottom:7px}input,select,textarea,button{font:inherit}input[type=text],input[type=password],input[type=number],select,textarea{width:100%;padding:11px 12px;border:1px solid var(--border);border-radius:9px;background:var(--panel);color:var(--text)}textarea{min-height:130px}button{border:0;border-radius:9px;padding:11px 18px;background:var(--accent);color:white;font-weight:700;cursor:pointer}.choice{display:flex;gap:10px;padding:9px 0}.error{color:var(--danger);font-size:.92rem}.status{display:flex;align-items:center;gap:12px;margin-top:24px}.spinner{width:18px;height:18px;border:3px solid var(--border);border-top-color:var(--accent);border-radius:50%;animation:spin .8s linear infinite}@keyframes spin{to{transform:rotate(360deg)}}#progress{margin:20px 0 0;padding:0;list-style:none}#progress li{padding:8px 0;border-top:1px solid var(--border)}pre{white-space:pre-wrap;overflow-wrap:anywhere}
</style></head><body><main class="shell"><section class="panel"><h1>{{.Title}}</h1>{{if .Description}}<p class="muted">{{.Description}}</p>{{end}}
{{if eq .Phase "loading"}}<div class="status"><span class="spinner"></span><strong>Loading form data…</strong></div><ul id="progress"></ul>
{{else if eq .Phase "running"}}<div class="status"><span class="spinner"></span><strong>Workflow is running…</strong></div><ul id="progress"></ul>
{{else if eq .Phase "ready"}}<form method="post" action="submit"><input type="hidden" name="csrf" value="{{.CSRF}}">
{{range $index,$field := .Fields}}<div class="field"><label for="{{fieldName $index}}">{{$field.Label}}{{if $field.Required}} *{{end}}</label>{{if $field.Description}}<div class="description">{{$field.Description}}</div>{{end}}
{{if $field.Options}}{{if eq $field.Type "array"}}{{range $choiceIndex,$choice := $field.Options}}<label class="choice"><input type="checkbox" name="{{fieldName $index}}" value="{{$choiceIndex}}" {{if selected $field $choice}}checked{{end}} {{if $choice.Disabled}}disabled{{end}}> <span>{{$choice.Label}}{{if $choice.Description}} — {{$choice.Description}}{{end}}{{if $choice.Disabled}} ({{$choice.Reason}}){{end}}</span></label>{{end}}{{else}}<select id="{{fieldName $index}}" name="{{fieldName $index}}"><option value="">Select…</option>{{range $choiceIndex,$choice := $field.Options}}<option value="{{$choiceIndex}}" {{if selected $field $choice}}selected{{end}} {{if $choice.Disabled}}disabled{{end}}>{{$choice.Label}}{{if $choice.Description}} — {{$choice.Description}}{{end}}</option>{{end}}</select>{{end}}
{{else if eq $field.Type "boolean"}}<label class="choice"><input id="{{fieldName $index}}" type="checkbox" name="{{fieldName $index}}" value="true" {{if checked $field}}checked{{end}}> <span>{{$field.Label}}</span></label>
{{else if eq $field.Type "number"}}<input id="{{fieldName $index}}" type="number" step="any" name="{{fieldName $index}}" value="{{stringValue $field}}">
{{else if or (eq $field.Type "array") (eq $field.Type "object")}}<textarea id="{{fieldName $index}}" name="{{fieldName $index}}">{{stringValue $field}}</textarea>
{{else}}<input id="{{fieldName $index}}" type="{{if $field.Secret}}password{{else}}text{{end}}" name="{{fieldName $index}}" value="{{stringValue $field}}">{{end}}
{{with fieldError $.Errors $field.Variable}}<div class="error">{{.}}</div>{{end}}</div>{{end}}<button type="submit">Run workflow</button></form>
{{else}}<h2>{{.ResultTitle}}</h2><div>{{.ResultHTML}}</div><p class="muted">Status: {{.Result.Status}} · {{.Result.Duration}}</p>{{end}}</section></main>
{{if or (eq .Phase "loading") (eq .Phase "running")}}<script src="app.js"></script>{{end}}
</body></html>`

const appJS = `const list=document.getElementById('progress');const source=new EventSource('events');source.onmessage=(event)=>{const state=JSON.parse(event.data);list.replaceChildren(...state.progress.slice(-12).map(p=>{const li=document.createElement('li');li.textContent=[p.stage,p.step_id||p.kind,p.status,p.duration].filter(Boolean).join(' · ');return li}));if(state.phase==='ready'||state.phase==='done'){source.close();location.reload()}};`

// ValuesFromQuery is exported for focused transport tests.
func ValuesFromQuery(raw string) (url.Values, error) { return url.ParseQuery(raw) }
