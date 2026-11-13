export type TrainSelection = {
  pinned: string[];
  active: string | null;
  pendingReplacement: string | null;
};

export type TrainSelectionAction =
  | { type: 'toggle'; identity: string; desktop: boolean }
  | { type: 'activate'; identity: string }
  | { type: 'replace'; identity: string }
  | { type: 'cancel-replacement' }
  | { type: 'clear' }
  | { type: 'sync'; identities: string[] }
  | { type: 'collapse' };

export const initialTrainSelection: TrainSelection = { pinned: [], active: null, pendingReplacement: null };

export function trainSelectionReducer(state: TrainSelection, action: TrainSelectionAction): TrainSelection {
  switch (action.type) {
    case 'toggle': {
      const index = state.pinned.indexOf(action.identity);
      if (index !== -1) {
        const pinned = state.pinned.filter(identity => identity !== action.identity);
        return { pinned, active: state.active === action.identity ? pinned.at(-1) ?? null : state.active,
          pendingReplacement: null };
      }
      if (!action.desktop) return { pinned: [action.identity], active: action.identity, pendingReplacement: null };
      if (state.pinned.length < 2) return { pinned: [...state.pinned, action.identity], active: action.identity, pendingReplacement: null };
      return { ...state, pendingReplacement: action.identity };
    }
    case 'activate':
      return state.pinned.includes(action.identity) ? { ...state, active: action.identity } : state;
    case 'replace': {
      if (!state.pendingReplacement) return state;
      const index = state.pinned.indexOf(action.identity);
      if (index === -1) return state;
      const pinned = [...state.pinned];
      pinned[index] = state.pendingReplacement;
      return { pinned, active: state.pendingReplacement, pendingReplacement: null };
    }
    case 'cancel-replacement':
      return state.pendingReplacement ? { ...state, pendingReplacement: null } : state;
    case 'clear':
      return initialTrainSelection;
    case 'collapse': {
      const active = state.active && state.pinned.includes(state.active) ? state.active : state.pinned.at(-1) ?? null;
      return active ? { pinned: [active], active, pendingReplacement: null } : initialTrainSelection;
    }
    case 'sync': {
      const accepted = new Set(action.identities);
      const pending = state.pendingReplacement && accepted.has(state.pendingReplacement) ? state.pendingReplacement : null;
      const missingSlots = state.pinned.map(identity => accepted.has(identity) ? identity : null);
      const completedReplacement = !!pending && missingSlots.includes(null);
      let pinned = missingSlots.filter((identity): identity is string => identity !== null);
      if (pending && pinned.length < 2) {
        const slot = missingSlots.indexOf(null);
        if (slot === -1) pinned = [...pinned, pending];
        else {
          missingSlots[slot] = pending;
          pinned = missingSlots.filter((identity): identity is string => identity !== null);
        }
      }
      const active = completedReplacement
        ? pending : state.active && pinned.includes(state.active) ? state.active : pinned.at(-1) ?? null;
      if (pinned.length === state.pinned.length && pinned.every((identity, index) => identity === state.pinned[index])
        && active === state.active && pending === state.pendingReplacement) return state;
      return { pinned, active, pendingReplacement: completedReplacement ? null : pending };
    }
  }
}
