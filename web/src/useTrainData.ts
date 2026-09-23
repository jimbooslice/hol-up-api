import { useEffect, useRef, useState } from 'react';
import { loadRouteGeometry, RouteGeometryError, type RouteGeometry, type RouteGeometryErrorKind } from './geometry';
import { loadPositions, needsGeometry, PositionsError, type PositionsErrorKind, type Snapshot } from './positions';

export function useTrainData() {
  const [geometry, setGeometry] = useState<RouteGeometry | null>(null);
  const [geometryError, setGeometryError] = useState<RouteGeometryErrorKind | null>(null);
  const [snapshot, setSnapshot] = useState<Snapshot | null>(null);
  const [failure, setFailure] = useState<PositionsErrorKind | null>(null);
  const [paused, setPaused] = useState(false);
  const [hidden, setHidden] = useState(document.hidden);
  const [retained, setRetained] = useState(true);
  const [now, setNow] = useState(() => performance.now());
  const [retry, setRetry] = useState(0);
  const initialAttempted = useRef(false);
  const geometryRef = useRef(geometry);
  const generation = useRef(0);
  const manualGeometry = useRef(false);

  useEffect(() => {
    const visibility = () => { setHidden(document.hidden); setRetained(true); };
    document.addEventListener('visibilitychange', visibility);
    return () => document.removeEventListener('visibilitychange', visibility);
  }, []);

  useEffect(() => {
    if (paused || hidden) return;
    setNow(performance.now());
    const timer = setInterval(() => setNow(performance.now()), 1000);
    return () => clearInterval(timer);
  }, [paused, hidden]);

  useEffect(() => {
    const current = ++generation.current;
    let active: AbortController | null = null;
    let deadline: ReturnType<typeof setTimeout> | undefined;
    let retryTimer: ReturnType<typeof setTimeout> | undefined;
    const live = () => generation.current === current;
    async function request<T>(load: (signal: AbortSignal) => Promise<T>): Promise<T> {
      const controller = new AbortController();
      active = controller;
      deadline = setTimeout(() => controller.abort(new DOMException('Request timed out', 'TimeoutError')), 10_000);
      try { return await load(controller.signal); }
      finally { clearTimeout(deadline); active = null; }
    }
    async function fetchGeometry() {
      try {
        const next = await request(signal => loadRouteGeometry('A', signal));
        if (!live()) return false;
        geometryRef.current = next;
        setGeometry(next); setGeometryError(null);
        return true;
      } catch (error) {
        if (live()) setGeometryError(error instanceof RouteGeometryError ? error.kind : 'network');
        return false;
      }
    }
    async function fetchTrains() {
      const startedAt = performance.now();
      try {
        const data = await request(loadPositions);
        if (!live()) return null;
        setSnapshot({ data, startedAt }); setNow(performance.now()); setFailure(null); setRetained(false);
        return data;
      } catch (error) {
        if (live()) { setFailure(error instanceof PositionsError ? error.kind : 'network'); setRetained(true); }
        return null;
      }
    }
    async function cycle() {
      if (!live()) return;
      if (!initialAttempted.current || manualGeometry.current) {
        manualGeometry.current = false;
        await fetchGeometry();
        if (!live()) return;
        initialAttempted.current = true;
      }
      const data = await fetchTrains();
      if (!live()) return;
      if (data && needsGeometry(geometryRef.current, data)) {
        const refreshed = await fetchGeometry();
        if (!live()) return;
        if (refreshed) await fetchTrains();
      }
      if (live()) retryTimer = setTimeout(() => { void cycle(); }, 5000);
    }
    if (!paused && !hidden) void cycle();
    return () => {
      ++generation.current;
      clearTimeout(deadline); clearTimeout(retryTimer); active?.abort();
    };
  }, [paused, hidden, retry]);

  return { geometry, geometryError, snapshot, failure, paused, hidden, retained, now,
    togglePause: () => { setRetained(true); setPaused(value => !value); },
    retryGeometry: () => { manualGeometry.current = true; setRetained(true); setRetry(value => value + 1); },
  };
}
