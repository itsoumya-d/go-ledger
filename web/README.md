# go-ledger web

A small React + TypeScript + Redux Toolkit + d3 control-plane view over the Go ledger API, plus a Devoli-token Usage Rating Studio for tiered billing, proration, GST and ledger posting.

## Stack

- React 18 + TypeScript
- Redux Toolkit (`ledger` slice)
- d3 bar chart of posting amounts
- Vite build
- CSS design tokens and a responsive single-column layout

## Run

```bash
npm ci
npm run dev
```

The app reads `GET /v1/accounts/customer_demo/balance` and falls back to sample posting data when the API is offline. `npm run build` type-checks with `tsc --noEmit` and produces `dist/`.

The CI `web` job runs `npm ci` and `npm run build` on every push.
