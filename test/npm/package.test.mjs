import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { mkdtempSync, readFileSync, readdirSync, rmSync, statSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';
import { binaryPath, targets } from '../../scripts/platforms.mjs';

test('the actual npm tarball installs without scripts, runs the CLI and contains buildable Go source', { timeout: 180_000 }, t => {
  const root = fileURLToPath(new URL('../../', import.meta.url));
  const scratch = mkdtempSync(path.join(tmpdir(), 'nugs npm package '));
  t.after(() => rmSync(scratch, { recursive: true, force: true }));
  assert.ok(process.env.npm_execpath, 'run this check with npm run test:package');
  const npm = args => execFileSync(process.execPath, [process.env.npm_execpath, ...args], { cwd: root, encoding: 'utf8' });
  const [packed] = JSON.parse(npm(['pack', '--ignore-scripts', '--json', '--pack-destination', scratch]));
  assert.equal(packed.name, '@mickmister/nugs');
  assert.equal(packed.filename, `mickmister-nugs-${packed.version}.tgz`);
  const files = new Set(packed.files.map(file => file.path));
  for (const name of [...readdirSync(root).filter(name => name.endsWith('.go')), 'go.mod', 'go.sum', 'cmd/nugs/main.go', 'bin/nugs.js']) {
    assert.ok(files.has(name), `published package is missing source/runtime file ${name}`);
  }
  for (const target of targets) {
    assert.ok(files.has(binaryPath(target.platform, target.arch).split(path.sep).join('/')));
  }
  for (const name of files) {
    assert.ok(!name.startsWith('.git') && !name.includes('node_modules/'), `unexpected private/build file ${name}`);
  }
  const prefix = path.join(scratch, 'installed');
  npm(['install', '--prefix', prefix, '--ignore-scripts', '--offline', '--no-audit', '--no-fund', path.join(scratch, packed.filename)]);
  const installed = path.join(prefix, 'node_modules', '@mickmister', 'nugs');
  const { version } = JSON.parse(readFileSync(path.join(installed, 'package.json')));
  const launcher = path.join(installed, 'bin', 'nugs.js');
  const run = args => execFileSync(process.execPath, [launcher, ...args], { cwd: scratch, encoding: 'utf8', stdio: 'pipe' });
  assert.equal(run(['--version']).trim(), `nugs ${version}`);
  assert.equal(npm(['exec', '--offline', '--prefix', prefix, '--', 'nugs', '--version']).trim(), `nugs ${version}`);
  assert.match(run(['--help']), /Usage:/);
  assert.throws(() => run(['unknown-command']), error => error.status === 2);
  assert.ok(statSync(path.join(installed, binaryPath(process.platform, process.arch))).size > 1024);
  // Rebuilding from the installed package proves source inclusion is complete,
  // rather than merely checking that a few .go filenames appear in a pack list.
  const rebuilt = path.join(scratch, process.platform === 'win32' ? 'rebuilt.exe' : 'rebuilt');
  execFileSync('go', ['build', '-mod=readonly', '-o', rebuilt, './cmd/nugs'], {
    cwd: installed, env: { ...process.env, GOWORK: 'off' }, stdio: 'pipe',
  });
  assert.match(execFileSync(rebuilt, ['--help'], { encoding: 'utf8' }), /Usage:/);
  console.log(`Verified @mickmister/nugs@${version}: ${packed.files.length} files, ${packed.size} compressed bytes, bundled Go source rebuild passed`);
});
