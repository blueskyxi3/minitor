import test from 'node:test';
import assert from 'node:assert/strict';
import { paths, requestParams, formatValue } from './data.mjs';

test('chart preserves missing observations and real zeros', () => {
  const geometry = paths([{ timestamp: 1, values: { value: 2 } }, { timestamp: 2, values: { value: null } }, { timestamp: 3, values: { value: 0 } }], ['value']);
  assert.equal((geometry.lines[0].d.match(/M/g) || []).length, 2);
  assert.ok(!geometry.lines[0].d.includes('L'));
  assert.equal(formatValue(null, 'ms', 'en'), '—');
  assert.equal(formatValue(0, 'ms', 'en'), '0');
});

test('30-day query stays below the API point limit and uses instance/model filters', () => {
  const params = requestParams({ live: true, range: 30 * 86400, scope: 'model', instance_id: 'slave-1', model_name: 'test' }, 1800000000000);
  assert.ok((params.end_timestamp - params.start_timestamp) / params.grain_seconds + 1 <= 1500);
  assert.equal(params.instance_id, 'slave-1');
  assert.equal(params.model_name, 'test');
  assert.throws(() => requestParams({ live: true, range: 31 * 86400 }), /invalid_range/);
});
