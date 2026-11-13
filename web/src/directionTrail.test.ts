import assert from 'node:assert/strict';
import { test } from 'node:test';
import { directionTrail } from './directionTrail.ts';
const project = ([x, y]: [number, number]) => ({ x, y });

test('dots follow ordered straight and curved path distance, even through repeated points', () => {
  assert.deepEqual(directionTrail([[0, 0], [100, 0]], { x: 100, y: 0 }, project),
    [{ x: 82, y: 0 }, { x: 74, y: 0 }, { x: 66, y: 0 }]);
  assert.deepEqual(directionTrail([[0, 0], [0, 20], [0, 20], [30, 20]], { x: 30, y: 20 }, project),
    [{ x: 12, y: 20 }, { x: 4, y: 20 }, { x: 0, y: 16 }]);
});

test('no trail when the traveled path is short, absent, or misses the marker', () => {
  assert.deepEqual(directionTrail([[0, 0], [33, 0]], { x: 33, y: 0 }, project), []);
  assert.deepEqual(directionTrail([], { x: 0, y: 0 }, project), []);
  assert.deepEqual(directionTrail([[0, 0], [100, 0]], { x: 100, y: 9 }, project), []);
  assert.equal(directionTrail([[0, 0], [100, 0]], { x: 100, y: 8 }, project).length, 3);
});
