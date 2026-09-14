# Hol Up web app

The React and TypeScript frontend is a self-contained package in the main
repository. It requires Node 24 and npm. During development it also expects the
Go API at `http://127.0.0.1:8080`; Vite proxies relative `/api` requests there.

```sh
# From web/
npm ci
npm run dev       # http://127.0.0.1:5173
npm run typecheck
npm run build     # writes dist/
```

Run the API from the repository root when developing both projects:

```sh
ADDR=127.0.0.1:8080 go run -buildvcs=false ./cmd/api
```

Runtime dependencies are React and React DOM. Vite, TypeScript, the React Vite
plugin, and type packages are build dependencies. The production container
builds `dist/` and the Go application serves it; Node is not in the runtime
image. Map rendering and API requests are intentionally deferred.
