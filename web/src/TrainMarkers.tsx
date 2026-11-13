import { useEffect, useState, type CSSProperties, type RefObject } from 'react';
import type { Map } from 'maplibre-gl';
import { routeStyle, type Train } from './positions';
import type { createTrainInteraction } from './trainInteraction';

export type ProjectedTrain = { train: Train; identity: string; x: number; y: number; accessibilityText: string;
  trail: { x: number; y: number }[]; directionLabel: string | null };
type Interaction = ReturnType<typeof createTrainInteraction>;
const trainHitSize = 44;

export function TrainMarkers({ markers, pinned, active, map, interaction, showDirections }: {
  markers: ProjectedTrain[]; pinned: string[]; active: string | null;
  map: Map | null; interaction: RefObject<Interaction | null>; showDirections: boolean;
}) {
  const [pointerHover, setPointerHover] = useState<string | null>(null);
  const viewport = { width: map?.getContainer().clientWidth ?? 0, height: map?.getContainer().clientHeight ?? 0 };
  const half = trainHitSize / 2;
  const visible = markers.filter(marker => Number.isFinite(marker.x) && Number.isFinite(marker.y)
    && marker.x >= half && marker.y >= half && marker.x <= viewport.width - half && marker.y <= viewport.height - half)
    .sort((left, right) => left.identity.localeCompare(right.identity));
  const hoveredRoute = visible.find(marker => marker.identity === pointerHover)?.train.route_id;
  useEffect(() => {
    if (pointerHover && (!hoveredRoute || pointerHover === active)) setPointerHover(null);
  }, [pointerHover, hoveredRoute, active]);
  const hiddenDuringHover = (marker: ProjectedTrain) => !!hoveredRoute && marker.train.route_id === hoveredRoute
    && marker.identity !== pointerHover && !pinned.includes(marker.identity);

  return <div className="train-marker-overlay" aria-label="Train markers" style={{ height: viewport.height }}>
    {showDirections && <div className="train-directions" aria-hidden="true">
      {visible.map(marker => {
        const style = routeStyle(marker.train.route_id);
        return <div className="train-direction-position" key={marker.identity} style={{ left: marker.x, top: marker.y,
          '--route-color': style.color } as CSSProperties} data-hover-hidden={hiddenDuringHover(marker) || undefined}
          data-active={active === marker.identity || undefined}>
          {marker.trail.map((dot, index) => <span className="train-direction-dot" key={index}
            style={{ left: dot.x - marker.x + half, top: dot.y - marker.y + half }} />)}
          {marker.directionLabel && <span className={`train-direction-chip${marker.x > viewport.width - 190 ? ' train-direction-chip-left' : ''}`}>{marker.directionLabel}</span>}
        </div>;
      })}
    </div>}
    <div className="train-badges">
      {visible.map(marker => {
        const style = routeStyle(marker.train.route_id);
        return <div className="train-marker-position" key={marker.identity} style={{ left: marker.x, top: marker.y }}
          data-hover-hidden={hiddenDuringHover(marker) || undefined}>
          <button type="button" className="train-marker" data-identity={marker.identity}
            data-simulation={marker.train.position_basis === 'schedule_fallback' || undefined}
            data-active={active === marker.identity || undefined}
            data-pinned={pinned.includes(marker.identity) || undefined}
            aria-pressed={pinned.includes(marker.identity)}
            aria-label={`${marker.accessibilityText}${active === marker.identity ? ', active journey' : pinned.includes(marker.identity) ? ', pinned train' : ''}`}
            style={{ '--route-color': style.color, '--route-text': style.text, width: trainHitSize, height: trainHitSize } as CSSProperties}
            onPointerEnter={event => { if (event.pointerType !== 'touch' && interaction.current?.preview(marker.identity, event.currentTarget)) setPointerHover(marker.identity); }}
            onPointerLeave={() => { if (interaction.current?.leave(marker.identity) !== false) setPointerHover(current => current === marker.identity ? null : current); }}
            onFocus={event => { setPointerHover(null); interaction.current?.preview(marker.identity, event.currentTarget); }}
            onBlur={() => interaction.current?.leave(marker.identity, true)}
            onClick={event => { setPointerHover(null); event.stopPropagation(); interaction.current?.select(marker.identity, event.currentTarget); }}>
            <span className="train-marker-badge" aria-hidden="true">{marker.train.route_id}</span>
          </button>
        </div>;
      })}
    </div>
  </div>;
}
