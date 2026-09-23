import { type CSSProperties, type RefObject } from 'react';
import type { Map } from 'maplibre-gl';
import { routeStyle, type Train } from './positions';
import type { createTrainInteraction } from './trainInteraction';

export type ProjectedTrain = { train: Train; identity: string; x: number; y: number; accessibilityText: string };
type Interaction = ReturnType<typeof createTrainInteraction>;
const trainHitSize = 44;

export function TrainMarkers({ markers, selected, map, interaction }: {
  markers: ProjectedTrain[]; selected: string | null;
  map: Map | null; interaction: RefObject<Interaction | null>;
}) {
  const viewport = { width: map?.getContainer().clientWidth ?? 0, height: map?.getContainer().clientHeight ?? 0 };
  const half = trainHitSize / 2;
  const visible = markers.filter(marker => Number.isFinite(marker.x) && Number.isFinite(marker.y)
    && marker.x >= half && marker.y >= half && marker.x <= viewport.width - half && marker.y <= viewport.height - half)
    .sort((left, right) => left.identity.localeCompare(right.identity));

  return <div className="train-marker-overlay" aria-label="Train markers" style={{ height: viewport.height }}>
    {visible.map(marker => {
      const style = routeStyle(marker.train.route_id);
      return <div className="train-marker-position" key={marker.identity} style={{ left: marker.x, top: marker.y }}>
        <button type="button" className="train-marker" data-identity={marker.identity}
          data-simulation={marker.train.position_basis === 'schedule_fallback' || undefined} aria-pressed={selected === marker.identity}
          aria-label={marker.accessibilityText}
          style={{ '--route-color': style.color, '--route-text': style.text, width: trainHitSize, height: trainHitSize } as CSSProperties}
          onPointerEnter={event => { if (event.pointerType !== 'touch') interaction.current?.preview(marker.identity, event.currentTarget); }}
          onPointerLeave={() => interaction.current?.leave(marker.identity)}
          onFocus={event => interaction.current?.preview(marker.identity, event.currentTarget)}
          onClick={event => { event.stopPropagation(); interaction.current?.select(marker.identity, event.currentTarget); }}>
          <span className="train-marker-badge" aria-hidden="true">{marker.train.route_id}</span>
        </button>
      </div>;
    })}
  </div>;
}
