# Script SDK bridge benchmarks

Measured on 2026-10-23 using an Apple M1 Max MacBook Pro (10 CPU cores, 64 GB RAM),
macOS 26.4, Go 1.26.8, Node.js 22.14.0, and Homebrew Bash 5.3.15.
The source was the working tree based on `00f47fe`, including uncommitted changes.

## Reproduce

```sh
just bench-script-sdk
```

This builds Wuko once, then runs five samples of each benchmark with a minimum
one-second measurement window. Go benchmarks can exceed this window when one
operation takes longer. Build time is excluded. Override Go benchmark flags by
appending arguments, for example:

```sh
just bench-script-sdk -bench 'BenchmarkSDKCall/node' -benchtime=2s
```

`WUKO_BENCH_NODE` and `WUKO_BENCH_BASH` select other runtime executables.
The harness resolves Node version-manager shims before timing. Bash uses the
actual built Wuko executable for every SDK function call.

The following tables show the median of five benchmark sample averages, with the
minimum and maximum sample averages in parentheses. These are wall-clock costs,
not CPU time or individual-call latency percentiles. Measurements run sequentially
without the race detector, with synthetic data and only PATH in the script
environment. The machine was shared with other work; load averages ranged from
roughly 7 to 12 during the main run. Treat the figures as local measurements,
not performance guarantees.

## Bridge lifecycle without starting a script

Each operation opens the bridge, optionally writes the adapter bootstrap and
descriptor, and closes the bridge, including removal of its temporary files.
The size is the synthetic string in `vars.payload`; the complete serialized
context is slightly larger.

| Payload | Bridge only | With Node adapter | With Bash adapter |
| --- | --- | --- | --- |
| 1 KiB | 0.262 ms (0.250–0.287) | 0.579 ms (0.576–0.591) | 0.588 ms (0.579–0.595) |
| 100 KiB | 0.367 ms (0.363–0.372) | 1.233 ms (1.217–1.267) | 1.230 ms (1.216–1.260) |
| 1 MiB | 1.522 ms (1.496–1.712) | 7.162 ms (7.066–7.239) | 7.241 ms (7.070–7.857) |

The adapter-inclusive columns include descriptor serialization and file writes.
They do not include the runtime reading, parsing, or freezing that descriptor.
Request template-renderer snapshots are absent in this synthetic fixture.

## Complete script invocation

Each operation starts and waits for the runtime. SDK cases also create, inject,
and close a bridge. The payload is 1 KiB. Scripts are invoked directly using
`node --input-type=module -e` or `bash --noprofile --norc -c`, without an extra
shell wrapper. These measurements exclude workflow loading, validation,
iteration-state cloning, and `collect` evaluation.

| Invocation | Node | Bash |
| --- | --- | --- |
| Plain empty script | 35.92 ms (35.77–36.74) | 15.88 ms (15.41–16.14) |
| Empty script with SDK | 41.01 ms (40.52–42.18) | 17.14 ms (16.53–17.86) |
| SDK script publishing one output | 49.99 ms (49.26–50.20) | 43.08 ms (36.47–55.53) |

Subtracting the medians gives an approximate SDK invocation increment of 5.09 ms
for Node and 1.26 ms for Bash before any calls. Publishing the first output adds
another 8.98 ms for Node and 25.94 ms for Bash. These differences are estimates
from separate sample groups, not paired measurements or isolated function timings.
The first call in a fresh process can cost more than a warmed call.

## Sequential calls within a running script

Each script warms its selected operation with 10 calls, then performs a timed
batch of 200 calls on one bridge. Host callbacks mark the batch boundaries, so
`ns/call` excludes bridge and runtime startup, warmup, and cleanup. The interval
includes the start callback's response and the end callback's request, about one
additional round trip per batch, left unsubtracted. `ns/op` in the raw Go output
is the entire script invocation and is not the per-call measurement.

Writes repeatedly replace the same key with the five-character string `hello`.
The helper operation calls the shared `upper` helper with `hello`. The harness
checks staged outputs/variables, checks the Node helper return value, and treats
process or SDK errors as benchmark failures.

| Operation, main run | Node | Bash |
| --- | --- | --- |
| Output | 1.560 ms (1.098–1.830) | 19.027 ms (18.746–22.695) |
| Set variable | 0.346 ms (0.288–1.733) | 19.861 ms (18.674–21.809) |
| Helper `upper` | 0.369 ms (0.312–0.779) | 21.239 ms (19.078–22.537) |
| Read context | Local snapshot access; no RPC | 18.474 ms (18.183–18.767) |

The Node output and variable samples overlapped a load spike. They use the same
transport and nearly identical host dispatch paths; these differences should not
be interpreted as an intrinsic cost difference between output and variable writes.
A separate Node-only follow-up below rechecks this variability after repository
tests and static checks completed.

| Node-only follow-up, two-second windows | Median | Sample range |
| --- | --- | --- |
| Output | 0.244 ms | 0.238–0.252 ms |
| Set variable | 0.247 ms | 0.241–0.250 ms |
| Helper `upper` | 0.322 ms | 0.272–0.369 ms |

The follow-up used the same source, binary, payload, warmup, and 200-call batches,
with five samples per operation. This puts output and variable writes close
together as expected. Node follow-up and Bash main-run figures were measured at
different times; avoid treating their ratio as a controlled paired comparison.

Unlike Node property access, Bash `wuko_get` launches Wuko and reads/parses the
descriptor. It avoids a socket request, but still pays the process startup cost.
Calls such as `step` and `runWorkflow` also execute application work and are not
represented by these transport-oriented measurements. Payload size, context size,
concurrent calls, and host load can all change the results.

## Applying this to foreach

Every SDK-enabled shell attempt repeats the invocation costs. For many small
items, batching work into one script amortizes runtime and bridge setup.
Node calls stay inside the running Node process until their socket request;
Bash starts Wuko for every SDK function call. Publishing a single result object
or array also reduces call count. Increasing `foreach.max_concurrency` overlaps
work but does not remove these costs.

The benchmarks do not exercise retries, the 64-call concurrency limit, large
per-call payloads, child workflows, or the complete foreach scheduler.

Raw samples: [main run](benchmarks/script-sdk-2026-10-23.txt) and
[Node follow-up](benchmarks/script-sdk-node-2026-10-23.txt).

Validation: `just test`, `just vet`, and `just lint` passed; `gofmt -l .` was empty.
