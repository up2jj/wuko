// Package tcpprobe implements portable TCP listener discovery for workflows.
package tcpprobe

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/workflow"
	"golang.org/x/sync/errgroup"
	"gopkg.in/yaml.v3"
)

const (
	defaultTimeout = time.Second
	probeLimit     = 16

	stateListening     = "listening"
	stateFree          = "free"
	stateIndeterminate = "indeterminate"
)

// Config selects the TCP endpoints to probe and an optional expected state.
type Config struct {
	Hosts   []string           `yaml:"hosts"`
	Ports   portList           `yaml:"ports"`
	Timeout *workflow.Duration `yaml:"timeout,omitempty"`
	Expect  string             `yaml:"expect,omitempty"`
	Owners  bool               `yaml:"owners,omitempty"`
}

// portList keeps ports as their YAML source text so a port can come from a variable. Every
// scalar node carries that text whatever its tag, so "ports: [8080]" and
// "ports: [{{ .vars.port }}]" decode through one path and are parsed once resolved.
type portList []string

func (list *portList) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.SequenceNode {
		return fmt.Errorf("ports must be a list of ports")
	}
	values := make(portList, 0, len(node.Content))
	for index, item := range node.Content {
		if item.Kind != yaml.ScalarNode {
			return fmt.Errorf("ports[%d] must be a port number or a template", index)
		}
		values = append(values, item.Value)
	}
	*list = values
	return nil
}

// targets are the probe endpoints after templates resolved and validation passed.
type targets struct {
	hosts []string
	ports []int
}

type Runner struct {
	config       Config
	dial         dialContextFunc
	localAddrs   func() (map[netip.Addr]struct{}, error)
	lookupOwners ownerLookupFunc
}

type dialContextFunc func(context.Context, string, string) (net.Conn, error)

type ownerLookupFunc func(context.Context, []listenerEndpoint) ownerLookupResult

type listenerEndpoint struct {
	address netip.Addr
	port    uint16
}

type owner struct {
	PID        int
	Name       string
	Executable string
}

type ownerLookupResult struct {
	supported bool
	complete  bool
	detail    string
	owners    map[listenerEndpoint][]owner
	// hint names a blind spot the scan hit, such as processes another user owns. It reaches
	// detail only when a listener ends up without an owner, so a lookup that found what it
	// was asked for stays quiet about processes that were never relevant.
	hint string
}

type probeResult struct {
	host    string
	port    int
	address string
	state   string
	detail  string
	matched bool
	owners  []owner
}

type expectationError struct {
	expect     string
	mismatches []string
}

func (err expectationError) Error() string {
	return fmt.Sprintf("TCP probe expected %s endpoints; mismatched: %s", err.expect, strings.Join(err.mismatches, ", "))
}

func (expectationError) ObservationAvailable() bool { return true }

// Register adds tcp_probe and its closed output contract to a registry.
func Register(registry *step.Registry) error {
	ownerSchema := step.ClosedOutputs("pid", "name", "executable")
	resultSchema := step.ClosedObject(map[string]step.OutputSchema{
		"host": step.Scalar(), "port": step.Scalar(), "address": step.Scalar(),
		"state": step.Scalar(), "matched": step.Scalar(), "detail": step.Scalar(),
		"owners": step.Array(ownerSchema),
	})
	return registry.RegisterDefinition("tcp_probe", step.Registration{
		Builder: New,
		Outputs: step.ClosedObject(map[string]step.OutputSchema{
			"count": step.Scalar(), "listening": step.Scalar(), "free": step.Scalar(),
			"indeterminate": step.Scalar(), "matched": step.Scalar(),
			"ownership": step.ClosedOutputs("requested", "supported", "complete", "detail"),
			"results":   step.Array(resultSchema),
		}),
	})
}

// New decodes and validates a TCP probe configuration.
func New(raw map[string]any) (step.Runner, error) {
	var config Config
	if err := step.DecodeConfig(raw, &config); err != nil {
		return nil, err
	}
	runner := &Runner{
		config:       config,
		dial:         (&net.Dialer{}).DialContext,
		localAddrs:   localAddresses,
		lookupOwners: nativeOwnerLookup,
	}
	if _, err := runner.validate(false); err != nil {
		return nil, err
	}
	return runner, nil
}

// Run probes every host and port pair and preserves declaration order in the result.
func (runner *Runner) Run(ctx context.Context, _ step.Request) (step.Result, error) {
	probes, err := runner.validate(true)
	if err != nil {
		return step.Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return step.Result{}, err
	}

	results := make([]probeResult, 0, len(probes.hosts)*len(probes.ports))
	for _, host := range probes.hosts {
		for _, port := range probes.ports {
			results = append(results, probeResult{host: host, port: port})
		}
	}

	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(probeLimit)
	for index := range results {
		group.Go(func() error {
			result, err := runner.probe(groupCtx, results[index].host, results[index].port)
			if err != nil {
				return err
			}
			results[index] = result
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return step.Result{}, err
	}

	ownership := ownerLookupResult{supported: ownerLookupSupported(), complete: !runner.config.Owners, owners: make(map[listenerEndpoint][]owner)}
	if runner.config.Owners {
		local, localErr := runner.localAddrs()
		endpoints := localListeningEndpoints(results, local)
		ownership = runner.lookupOwners(ctx, endpoints)
		if err := ctx.Err(); err != nil {
			return step.Result{}, err
		}
		if ownership.owners == nil {
			ownership.owners = make(map[listenerEndpoint][]owner)
		}
		noteMissingOwners(&ownership, endpoints)
		attachOwners(results, ownership.owners)
		if localErr != nil {
			ownership.complete = false
			ownership.detail = joinDetail(ownership.detail, fmt.Sprintf("enumerating local addresses: %v", localErr))
		}
	}

	outputs, mismatches := runner.outputs(results, ownership)
	result := step.Result{Outputs: outputs}
	if runner.config.Expect != "" && len(mismatches) > 0 {
		return result, expectationError{expect: runner.config.Expect, mismatches: mismatches}
	}
	return result, nil
}

func (runner *Runner) probe(ctx context.Context, host string, port int) (probeResult, error) {
	result := probeResult{host: host, port: port}
	probeCtx, cancel := context.WithTimeout(ctx, runner.timeout())
	defer cancel()

	connection, err := runner.dial(probeCtx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err == nil {
		result.state = stateListening
		result.address = remoteAddress(connection.RemoteAddr())
		if closeErr := connection.Close(); closeErr != nil {
			result.detail = fmt.Sprintf("closing probe connection: %v", closeErr)
		}
		result.matched = runner.matches(result.state)
		return result, nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return probeResult{}, ctxErr
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		result.state = stateFree
		result.detail = "connection refused"
	} else {
		result.state = stateIndeterminate
		if errors.Is(probeCtx.Err(), context.DeadlineExceeded) {
			result.detail = fmt.Sprintf("probe timed out after %s", runner.timeout())
		} else {
			result.detail = err.Error()
		}
	}
	result.matched = runner.matches(result.state)
	return result, nil
}

func (runner *Runner) outputs(results []probeResult, ownership ownerLookupResult) (map[string]any, []string) {
	items := make([]any, len(results))
	counts := map[string]int{stateListening: 0, stateFree: 0, stateIndeterminate: 0}
	var mismatches []string
	for index, result := range results {
		counts[result.state]++
		owners := make([]any, len(result.owners))
		for ownerIndex, process := range result.owners {
			owners[ownerIndex] = map[string]any{
				"pid": process.PID, "name": process.Name, "executable": process.Executable,
			}
		}
		items[index] = map[string]any{
			"host": result.host, "port": result.port, "address": result.address,
			"state": result.state, "matched": runner.reportMatch(result.matched), "detail": result.detail,
			"owners": owners,
		}
		if !result.matched {
			mismatches = append(mismatches, net.JoinHostPort(result.host, strconv.Itoa(result.port))+" ("+result.state+")")
		}
	}
	return map[string]any{
		"count": len(results), "listening": counts[stateListening], "free": counts[stateFree],
		"indeterminate": counts[stateIndeterminate], "matched": runner.reportMatch(len(mismatches) == 0),
		"ownership": map[string]any{
			"requested": runner.config.Owners, "supported": ownership.supported,
			"complete": ownership.complete, "detail": ownership.detail,
		},
		"results": items,
	}, mismatches
}

// validate checks the configuration and, once templates have resolved, returns the endpoints
// to probe. Before resolution the templated entries are skipped rather than rejected, so the
// returned targets are only complete, and only used, when resolved is true.
func (runner *Runner) validate(resolved bool) (targets, error) {
	hosts, err := runner.validateHosts(resolved)
	if err != nil {
		return targets{}, err
	}
	ports, err := runner.validatePorts(resolved)
	if err != nil {
		return targets{}, err
	}
	if runner.config.Timeout != nil && runner.config.Timeout.Value() <= 0 {
		return targets{}, fmt.Errorf("timeout must be greater than zero")
	}
	if resolved || !unresolved(runner.config.Expect) {
		if !slices.Contains([]string{"", stateFree, stateListening}, runner.config.Expect) {
			return targets{}, fmt.Errorf("expect must be free or listening")
		}
	}
	return targets{hosts: hosts, ports: ports}, nil
}

func (runner *Runner) validateHosts(resolved bool) ([]string, error) {
	if len(runner.config.Hosts) == 0 {
		return nil, fmt.Errorf("hosts must contain at least one host")
	}
	hosts := make([]string, 0, len(runner.config.Hosts))
	seen := make(map[string]struct{}, len(runner.config.Hosts))
	for index, raw := range runner.config.Hosts {
		// Surrounding whitespace is dropped rather than dialed, so " 127.0.0.1" reports a
		// duplicate or an empty host instead of becoming an indeterminate probe.
		host := strings.TrimSpace(raw)
		if !resolved && unresolved(host) {
			continue
		}
		if host == "" {
			return nil, fmt.Errorf("hosts[%d] must not be empty", index)
		}
		if _, duplicate := seen[host]; duplicate {
			return nil, fmt.Errorf("hosts contains duplicate %q", host)
		}
		seen[host] = struct{}{}
		hosts = append(hosts, host)
	}
	return hosts, nil
}

func (runner *Runner) validatePorts(resolved bool) ([]int, error) {
	if len(runner.config.Ports) == 0 {
		return nil, fmt.Errorf("ports must contain at least one port")
	}
	ports := make([]int, 0, len(runner.config.Ports))
	seen := make(map[int]struct{}, len(runner.config.Ports))
	for index, raw := range runner.config.Ports {
		text := strings.TrimSpace(raw)
		if !resolved && unresolved(text) {
			continue
		}
		port, err := strconv.Atoi(text)
		if err != nil {
			return nil, fmt.Errorf("ports[%d] must be a port number, got %q", index, raw)
		}
		if port < 1 || port > 65535 {
			return nil, fmt.Errorf("ports[%d] must be between 1 and 65535", index)
		}
		if _, duplicate := seen[port]; duplicate {
			return nil, fmt.Errorf("ports contains duplicate %d", port)
		}
		seen[port] = struct{}{}
		ports = append(ports, port)
	}
	return ports, nil
}

func (runner *Runner) timeout() time.Duration {
	if runner.config.Timeout == nil {
		return defaultTimeout
	}
	return runner.config.Timeout.Value()
}

func (runner *Runner) matches(state string) bool {
	return runner.config.Expect == "" || runner.config.Expect == state
}

// reportMatch publishes a match only when there is an expectation to match against. Without
// expect every state satisfies matches, so reporting true would tell a workflow that reads
// results[].matched as "is this port in use" exactly what it wants to hear, whatever the
// state. A null says the question was never asked.
func (runner *Runner) reportMatch(matched bool) any {
	if runner.config.Expect == "" {
		return nil
	}
	return matched
}

func localAddresses() (map[netip.Addr]struct{}, error) {
	addresses := map[netip.Addr]struct{}{
		netip.MustParseAddr("127.0.0.1"): {},
		netip.IPv6Loopback():             {},
	}
	interfaces, err := net.Interfaces()
	if err != nil {
		return addresses, err
	}
	var enumerationErr error
	for _, item := range interfaces {
		assigned, err := item.Addrs()
		if err != nil {
			enumerationErr = errors.Join(enumerationErr, err)
			continue
		}
		for _, value := range assigned {
			prefix, err := netip.ParsePrefix(value.String())
			if err == nil {
				addresses[prefix.Addr().Unmap()] = struct{}{}
			}
		}
	}
	return addresses, enumerationErr
}

func localListeningEndpoints(results []probeResult, local map[netip.Addr]struct{}) []listenerEndpoint {
	seen := make(map[listenerEndpoint]struct{})
	var endpoints []listenerEndpoint
	for _, result := range results {
		if result.state != stateListening {
			continue
		}
		address, err := netip.ParseAddr(result.address)
		if err != nil {
			continue
		}
		address = address.Unmap()
		if _, exists := local[address]; !exists && !address.IsLoopback() {
			continue
		}
		endpoint := listenerEndpoint{address: address, port: uint16(result.port)}
		if _, duplicate := seen[endpoint]; duplicate {
			continue
		}
		seen[endpoint] = struct{}{}
		endpoints = append(endpoints, endpoint)
	}
	return endpoints
}

// noteMissingOwners marks a lookup incomplete when a listener has no owning process. Every
// endpoint handed to the lookup completed a TCP handshake moments earlier, so an empty owner
// list never means "nobody owns this port"; it means the scan could not see the owner. A
// doctor-style workflow acts on that difference, so it must not be reported as a clean result.
func noteMissingOwners(result *ownerLookupResult, endpoints []listenerEndpoint) {
	var missing []string
	for _, endpoint := range endpoints {
		if len(result.owners[endpoint]) == 0 {
			missing = append(missing, netip.AddrPortFrom(endpoint.address, endpoint.port).String())
		}
	}
	if len(missing) == 0 {
		return
	}
	result.complete = false
	result.detail = joinDetail(result.detail, "no owning process found for "+strings.Join(missing, ", "))
	result.detail = joinDetail(result.detail, result.hint)
}

func attachOwners(results []probeResult, processes map[listenerEndpoint][]owner) {
	for index := range results {
		address, err := netip.ParseAddr(results[index].address)
		if err != nil {
			continue
		}
		endpoint := listenerEndpoint{address: address.Unmap(), port: uint16(results[index].port)}
		results[index].owners = processes[endpoint]
	}
}

func remoteAddress(address net.Addr) string {
	if address == nil {
		return ""
	}
	host, _, err := net.SplitHostPort(address.String())
	if err != nil {
		return ""
	}
	parsed, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	return parsed.Unmap().String()
}

func unresolved(value string) bool {
	return strings.Contains(value, "{{") || strings.Contains(value, "}}")
}

func joinDetail(current, next string) string {
	if current == "" {
		return next
	}
	if next == "" {
		return current
	}
	return current + "; " + next
}
