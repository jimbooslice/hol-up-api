Synthetic, offline A-line ingestion fixture (no captured operational payload).

`a-line.zip` contains six minimal GTFS tables with Sunday service in 2026. The
trip ID uses the supplied current-prefix example
`BSP26GEN-A055-Sunday-00_000950_A..S74R`; its stops depart at 00:09:30 and 00:19:30
New York service time. Coordinates and stopping pattern are test data.

`a-line.pb` is a GTFS-Realtime 2.0 FULL_DATASET at 2026-09-13 04:09:00 UTC, with
NYCT extension version 1.0, an A replacement window with omitted start and end
04:39:00 UTC, and a southbound `000950_A..S74R` trip update for 20260913. Its
sequence-only first stop predicts 04:09:30 UTC. Compact selection remains empty;
reconciliation still identifies an active counterpart.
