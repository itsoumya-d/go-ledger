import { useEffect, useRef } from "react";
import * as d3 from "d3";
import { useDispatch, useSelector } from "react-redux";
import { setBalance, setLoading, setOffline } from "./store";
import type { AppDispatch, RootState } from "./store";
import { RatingStudio } from "./RatingStudio";

export function App() {
  const dispatch = useDispatch<AppDispatch>();
  const { balanceCents, postings, status } = useSelector((state: RootState) => state.ledger);
  const chartRef = useRef<SVGSVGElement | null>(null);

  useEffect(() => {
    dispatch(setLoading());
    fetch("/v1/accounts/customer_demo/balance")
      .then((response) => (response.ok ? response.json() : Promise.reject(response)))
      .then((data: { balance_cents: number }) => dispatch(setBalance(data.balance_cents)))
      .catch(() => dispatch(setOffline()));
  }, [dispatch]);

  useEffect(() => {
    if (!chartRef.current) return;
    const svg = d3.select(chartRef.current);
    svg.selectAll("*").remove();

    const width = 560;
    const height = 220;
    const margin = { top: 16, right: 16, bottom: 28, left: 52 };
    const usableWidth = width - margin.left - margin.right;
    const usableHeight = height - margin.top - margin.bottom;

    const x = d3
      .scaleBand()
      .domain(postings.map((posting, index) => `${posting.intentId}-${index}`))
      .range([0, usableWidth])
      .padding(0.25);
    const max = d3.max(postings, (posting) => Math.abs(posting.amountCents)) ?? 1;
    const y = d3.scaleLinear().domain([0, max]).nice().range([usableHeight, 0]);

    const group = svg
      .attr("viewBox", `0 0 ${width} ${height}`)
      .attr("role", "img")
      .attr("aria-label", "Posting amounts by intent")
      .append("g")
      .attr("transform", `translate(${margin.left},${margin.top})`);

    group
      .selectAll("rect")
      .data(postings)
      .join("rect")
      .attr("x", (_posting, index) => x(`${postings[index].intentId}-${index}`) ?? 0)
      .attr("y", (posting) => y(Math.abs(posting.amountCents)))
      .attr("width", x.bandwidth())
      .attr("height", (posting) => usableHeight - y(Math.abs(posting.amountCents)))
      .attr("fill", (posting) => (posting.amountCents < 0 ? "#b42318" : "#067647"))
      .attr("rx", 2);

    group
      .append("g")
      .attr("transform", `translate(0,${usableHeight})`)
      .call(d3.axisBottom(x).tickFormat((_value, index) => postings[index]?.intentId ?? "").tickSizeOuter(0));

    group.append("g").call(d3.axisLeft(y).ticks(4).tickFormat((value) => `$${Number(value) / 100}`));
  }, [postings]);

  return (
    <main className="shell">
      <header className="hero">
        <div>
          <p className="eyebrow">go-ledger · Open Banking reference</p>
          <h1>Ledger Control Plane</h1>
          <p className="lede">
            Idempotent payment intents, double-entry postings, and an auditable event trail. This
            surface is a React + Redux + d3 view over the Go API.
          </p>
        </div>
        <div className="status" data-testid="status">
          {status === "offline" ? "API offline · sample data" : "API connected"}
        </div>
      </header>

      <section className="cards">
        <article className="card">
          <p className="label">Demo balance</p>
          <p className="value" data-testid="balance">
            ${(balanceCents / 100).toLocaleString("en-US", { minimumFractionDigits: 2 })}
          </p>
          <p className="hint">customer_demo · USD</p>
        </article>
        <article className="card">
          <p className="label">Postings</p>
          <p className="value" data-testid="posting-count">
            {postings.length}
          </p>
          <p className="hint">double-entry · debit + credit</p>
        </article>
        <article className="card">
          <p className="label">Invariant</p>
          <p className="value">Σ 0</p>
          <p className="hint">every settlement nets to zero</p>
        </article>
      </section>

      <section className="panel">
        <div className="panel-head">
          <h2>Posting amounts</h2>
          <span>debits negative · credits positive</span>
        </div>
        <svg ref={chartRef} className="chart" />
      </section>

      <RatingStudio />

      <section className="panel">
        <div className="panel-head">
          <h2>API surface</h2>
          <span>same-origin Go service</span>
        </div>
        <ul className="endpoints">
          <li><code>POST /openbanking/v1/consents</code> consent-bound access</li>
          <li><code>POST /openbanking/v1/payments</code> idempotent settlement</li>
          <li><code>POST /graphql</code> intents, balances, postings</li>
          <li><code>GET /metrics</code> Prometheus counters and latency</li>
        </ul>
      </section>
    </main>
  );
}
