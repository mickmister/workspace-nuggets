import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';

test('release does not create GitHub Actions workflow artifacts or enumerate all secrets', () => {
  const workflow = readFileSync(new URL('../workflows/publish-npm.yml', import.meta.url), 'utf8');
  assert.doesNotMatch(workflow, /actions\/(?:upload|download)-artifact@/);
  assert.doesNotMatch(workflow, /toJSON\(secrets\)/);
});
