package scriptsdk

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// RunBashClient executes one shell function call. Its arguments contain values,
// never shell source. Successful writes produce no stdout; reads produce a raw
// string or JSON followed by a newline. JSON variants always return JSON.
func RunBashClient(ctx context.Context, descriptorPath string, args []string, out io.Writer) error {
	method, params, err := bashCall(args)
	if err != nil {
		return err
	}
	result, err := requestBridge(ctx, descriptorPath, method, params)
	if err != nil {
		return err
	}
	operation := args[0]
	if operation == "output" || operation == "output-json" || operation == "set-var" || operation == "set-var-json" {
		return nil
	}
	if operation == "get" || operation == "get-json" {
		if len(args) == 2 {
			result, err = contextPath(result, args[1])
			if err != nil {
				return err
			}
		}
	}
	forceJSON := operation == "get-json" || operation == "helper-json" || operation == "step" || operation == "run-workflow" || operation == "call"
	// Only a JSON string needs decoding, to unescape it; everything else is
	// written as received.
	if !forceJSON && len(result) > 0 && result[0] == '"' {
		var value string
		if err := json.Unmarshal(result, &value); err != nil {
			return fmt.Errorf("decoding SDK result: %w", err)
		}
		_, err = io.WriteString(out, value+"\n")
		return err
	}
	if _, err = out.Write(result); err == nil {
		_, err = io.WriteString(out, "\n")
	}
	return err
}

func bashCall(args []string) (string, any, error) {
	if len(args) == 0 {
		return "", nil, fmt.Errorf("script SDK operation is required")
	}
	for _, arg := range args {
		if !utf8.ValidString(arg) {
			return "", nil, fmt.Errorf("script SDK arguments must be UTF-8 text")
		}
	}
	operation, values := args[0], args[1:]
	invalid := func(usage string) (string, any, error) {
		return "", nil, fmt.Errorf("usage: %s", usage)
	}
	switch operation {
	case "output", "output-json", "set-var", "set-var-json":
		if len(values) != 2 {
			return invalid("wuko_" + strings.ReplaceAll(operation, "-", "_") + " NAME VALUE")
		}
		var value any = values[1]
		if strings.HasSuffix(operation, "-json") {
			if err := decode([]byte(values[1]), &value); err != nil {
				return "", nil, fmt.Errorf("value must be valid JSON: %w", err)
			}
		}
		method := "output"
		if strings.HasPrefix(operation, "set-var") {
			method = "set_var"
		}
		return method, map[string]any{"name": values[0], "value": value}, nil
	case "get", "get-json":
		if len(values) > 1 {
			return invalid("wuko_get[_json] [DOT_PATH]")
		}
		return "context", struct{}{}, nil
	case "helper", "helper-json":
		if len(values) == 0 || (operation == "helper-json" && len(values) != 2) {
			return invalid("wuko_helper NAME [STRING ...] or wuko_helper_json NAME JSON_ARRAY")
		}
		arguments := make([]any, 0, len(values)-1)
		if operation == "helper-json" {
			if err := decode([]byte(values[1]), &arguments); err != nil || arguments == nil {
				return "", nil, fmt.Errorf("helper arguments must be a JSON array")
			}
		} else {
			for _, value := range values[1:] {
				arguments = append(arguments, value)
			}
		}
		return "helper", map[string]any{"name": values[0], "args": arguments}, nil
	case "secret":
		if len(values) != 1 {
			return invalid("wuko_secret REFERENCE")
		}
		return "secret", map[string]any{"reference": values[0]}, nil
	case "render", "step":
		if len(values) < 1 || len(values) > 2 {
			return invalid("wuko_render TEMPLATE [EXTRA_JSON_OBJECT] or wuko_step TYPE [CONFIG_JSON_OBJECT]")
		}
		object, err := bashObject(values, 1)
		if err != nil {
			return "", nil, err
		}
		if operation == "render" {
			return "render", map[string]any{"template": values[0], "extra": object}, nil
		}
		return "step", map[string]any{"type": values[0], "config": object}, nil
	case "run-workflow":
		if len(values) < 1 || len(values) > 3 {
			return invalid("wuko_run_workflow NAME [VARS_JSON_OBJECT [TARGET]]")
		}
		variables, err := bashObject(values, 1)
		if err != nil {
			return "", nil, err
		}
		target := ""
		if len(values) == 3 {
			target = values[2]
		}
		return "run_workflow", map[string]any{"name": values[0], "vars": variables, "target": target}, nil
	case "call":
		if len(values) != 2 {
			return invalid("wuko_call METHOD PARAMS_JSON_OBJECT")
		}
		params, err := bashObject(values, 1)
		return values[0], params, err
	default:
		return "", nil, fmt.Errorf("unknown script SDK operation %q", operation)
	}
}

func bashObject(values []string, index int) (map[string]any, error) {
	object := map[string]any{}
	if len(values) > index {
		if err := decode([]byte(values[index]), &object); err != nil || object == nil {
			return nil, fmt.Errorf("argument must be a JSON object")
		}
	}
	return object, nil
}

// contextPath descends one level at a time, keeping unvisited values as raw
// bytes instead of decoding the whole context into a generic tree.
func contextPath(data json.RawMessage, path string) (json.RawMessage, error) {
	value := bytes.TrimSpace(data)
	for _, part := range strings.Split(path, ".") {
		if len(value) == 0 {
			return nil, fmt.Errorf("cannot traverse SDK context path %q", path)
		}
		switch value[0] {
		case '{':
			var current map[string]json.RawMessage
			if err := json.Unmarshal(value, &current); err != nil {
				return nil, fmt.Errorf("decoding SDK context: %w", err)
			}
			var exists bool
			value, exists = current[part]
			if !exists {
				return nil, fmt.Errorf("SDK context path %q does not exist", path)
			}
		case '[':
			var current []json.RawMessage
			if err := json.Unmarshal(value, &current); err != nil {
				return nil, fmt.Errorf("decoding SDK context: %w", err)
			}
			index, err := strconv.Atoi(part)
			if err != nil || index < 0 || index >= len(current) {
				return nil, fmt.Errorf("invalid array index in SDK context path %q", path)
			}
			value = current[index]
		default:
			return nil, fmt.Errorf("cannot traverse SDK context path %q", path)
		}
	}
	return value, nil
}

func requestBridge(ctx context.Context, descriptorPath, method string, params any) (json.RawMessage, error) {
	descriptor, err := readDescriptor(descriptorPath)
	if err != nil {
		return nil, err
	}
	if method == "context" && params == (struct{}{}) {
		// The descriptor already holds the immutable snapshot the context call
		// would return; reuse it instead of a socket round trip.
		return descriptor.Context, nil
	}
	return postBridge(ctx, descriptor, method, params)
}

func readDescriptor(descriptorPath string) (Descriptor, error) {
	if descriptorPath == "" {
		return Descriptor{}, fmt.Errorf("script SDK is unavailable: %s is not set", DescriptorEnvironment)
	}
	file, err := os.Open(descriptorPath)
	if err != nil {
		return Descriptor{}, fmt.Errorf("opening SDK descriptor: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxMessageBytes+8193))
	if err != nil || len(data) > maxMessageBytes+8192 {
		return Descriptor{}, fmt.Errorf("cannot read SDK descriptor or it exceeds its size limit")
	}
	var descriptor Descriptor
	if err := decode(data, &descriptor); err != nil || descriptor.Version != Version || descriptor.Socket == "" || descriptor.Token == "" || len(descriptor.Context) == 0 {
		return Descriptor{}, fmt.Errorf("invalid SDK descriptor: expected bridge version %d, socket, token and context", Version)
	}
	return descriptor, nil
}

func postBridge(ctx context.Context, descriptor Descriptor, method string, params any) (json.RawMessage, error) {
	parameters, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("encoding SDK parameters: %w", err)
	}
	body, err := json.Marshal(callRequest{Version: Version, Method: method, Params: parameters})
	if err != nil {
		return nil, fmt.Errorf("encoding SDK request: %w", err)
	}
	if len(body) > maxMessageBytes {
		return nil, fmt.Errorf("SDK request exceeds 10 MiB")
	}
	dialer := net.Dialer{Timeout: 5 * time.Second}
	transport := &http.Transport{
		DisableKeepAlives: true,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, "unix", descriptor.Socket)
		},
	}
	defer transport.CloseIdleConnections()
	// Host calls may run long workflows. Their lifetime is bound to the command
	// context and owning attempt rather than an arbitrary HTTP response timeout.
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://wuko/v1/call", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+descriptor.Token)
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("calling SDK bridge: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("SDK bridge returned HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxMessageBytes+1))
	if err != nil || len(data) > maxMessageBytes {
		return nil, fmt.Errorf("cannot read SDK response or it exceeds 10 MiB")
	}
	var envelope struct {
		Version int             `json:"version"`
		Result  json.RawMessage `json:"result"`
		Error   *callError      `json:"error"`
	}
	if err := decode(data, &envelope); err != nil || envelope.Version != Version || (envelope.Error != nil && len(envelope.Result) != 0) {
		return nil, fmt.Errorf("invalid SDK response: expected bridge version %d", Version)
	}
	if envelope.Error != nil {
		return nil, fmt.Errorf("SDK %s: %s", envelope.Error.Code, envelope.Error.Message)
	}
	if len(envelope.Result) == 0 {
		return json.RawMessage("null"), nil
	}
	return envelope.Result, nil
}
