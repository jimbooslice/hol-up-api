import assert from 'node:assert/strict';
import test from 'node:test';
import { initialTrainSelection, trainSelectionReducer as reduce, type TrainSelection } from './trainSelection.ts';

const toggle = (state: TrainSelection, identity: string, desktop = true) => reduce(state, { type: 'toggle', identity, desktop });

test('pins in order, promotes the newest remaining pin, and clears all', () => {
  let state = toggle(initialTrainSelection, 'a');
  state = toggle(state, 'b');
  assert.deepEqual(state, { pinned: ['a', 'b'], active: 'b', pendingReplacement: null });
  state = reduce(state, { type: 'activate', identity: 'a' });
  assert.equal(state.active, 'a');
  state = toggle(state, 'a');
  assert.deepEqual(state, { pinned: ['b'], active: 'b', pendingReplacement: null });
  assert.deepEqual(reduce(state, { type: 'clear' }), initialTrainSelection);
});

test('replaces the chosen slot, makes the incoming train active, or cancels', () => {
  const full = toggle(toggle(initialTrainSelection, 'a'), 'b');
  const pending = toggle(full, 'c');
  assert.equal(pending.pendingReplacement, 'c');
  assert.deepEqual(reduce(pending, { type: 'replace', identity: 'a' }), {
    pinned: ['c', 'b'], active: 'c', pendingReplacement: null,
  });
  assert.deepEqual(reduce(pending, { type: 'cancel-replacement' }), full);
});

test('accepted snapshots remove missing trains and fill a vacated replacement slot', () => {
  const pending = toggle(toggle(toggle(initialTrainSelection, 'a'), 'b'), 'c');
  assert.deepEqual(reduce(pending, { type: 'sync', identities: ['b', 'c'] }), {
    pinned: ['c', 'b'], active: 'c', pendingReplacement: null,
  });
  assert.deepEqual(reduce({ pinned: ['a', 'b'], active: 'a', pendingReplacement: null }, { type: 'sync', identities: ['b'] }), {
    pinned: ['b'], active: 'b', pendingReplacement: null,
  });
  assert.deepEqual(reduce(pending, { type: 'sync', identities: ['a', 'b'] }), {
    pinned: ['a', 'b'], active: 'b', pendingReplacement: null,
  });
});

test('phone toggles replace the selection and responsive collapse retains the active pin', () => {
  const desktop = reduce(toggle(toggle(initialTrainSelection, 'a'), 'b'), { type: 'activate', identity: 'a' });
  assert.deepEqual(reduce(desktop, { type: 'collapse' }), { pinned: ['a'], active: 'a', pendingReplacement: null });
  assert.deepEqual(toggle({ pinned: ['a'], active: 'a', pendingReplacement: null }, 'b', false), {
    pinned: ['b'], active: 'b', pendingReplacement: null,
  });
});
