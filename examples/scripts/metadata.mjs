// Wuko injects globalThis.wuko before this module runs. No npm package is needed.
const label = await wuko.helpers.upper(wuko.args.name);
const result = await wuko.step('shell', {
  command: 'printf',
  args: ['%s', label],
  stdout: 'capture',
});

console.log(`Preparing ${wuko.args.packages.length} packages`);
await wuko.output('label', result.stdout);
await wuko.output('packages', wuko.args.packages);
await wuko.setVar('release_label', label);
