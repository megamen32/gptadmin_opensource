const fs = require('node:fs');

const names = ['ACTIONS_RUNTIME_TOKEN', 'ACTIONS_CACHE_URL', 'ACTIONS_RESULTS_URL', 'ACTIONS_CACHE_SERVICE_V2'];
if (!process.env.GITHUB_ENV || !process.env.ACTIONS_RUNTIME_TOKEN) {
  throw new Error('GitHub job cache runtime is unavailable');
}
for (const name of names) {
  const value = process.env[name];
  if (!value) continue;
  if (/[\r\n]/.test(value)) throw new Error('Invalid cache runtime value');
  if (name === 'ACTIONS_RUNTIME_TOKEN') {
    process.stdout.write('::add-mask::' + value.replace(/%/g, '%25') + '\n');
  }
  fs.appendFileSync(process.env.GITHUB_ENV, name + '=' + value + '\n');
}
