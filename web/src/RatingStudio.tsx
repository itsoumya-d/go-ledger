import { useEffect, useState } from "react";

type Plan = {
  id: string;
  name: string;
  base_monthly_cents: number;
  included_units: number;
  tax_rate_bps: number;
  currency: string;
};

type InvoiceLine = {
  description: string;
  units: number;
  unit_price_cents: number;
  amount_cents: number;
};

type RatedInvoice = {
  id: string;
  account_id: string;
  plan_id: string;
  usage_units: number;
  active_days: number;
  base_cents: number;
  usage_cents: number;
  subtotal_cents: number;
  tax_cents: number;
  total_cents: number;
  currency: string;
  status: string;
  intent_id?: string;
  lines: InvoiceLine[];
};

const fallbackPlans: Plan[] = [
  {
    id: "wholesale_100",
    name: "Wholesale Broadband 100",
    base_monthly_cents: 4900,
    included_units: 100,
    tax_rate_bps: 1500,
    currency: "NZD",
  },
  {
    id: "voice_seat",
    name: "Voice Seat",
    base_monthly_cents: 2500,
    included_units: 0,
    tax_rate_bps: 1500,
    currency: "NZD",
  },
];

function money(cents: number, currency: string) {
  return new Intl.NumberFormat("en-NZ", { style: "currency", currency }).format(cents / 100);
}

function currentPeriod() {
  const now = new Date();
  const year = now.getFullYear();
  const month = now.getMonth();
  const days = new Date(year, month + 1, 0).getDate();
  const prefix = `${year}-${String(month + 1).padStart(2, "0")}`;
  return { start: `${prefix}-01`, end: `${prefix}-${String(days).padStart(2, "0")}`, days };
}

export function RatingStudio() {
  const period = currentPeriod();
  const [plans, setPlans] = useState<Plan[]>(fallbackPlans);
  const [planId, setPlanId] = useState("wholesale_100");
  const [usage, setUsage] = useState(1200);
  const [activeDays, setActiveDays] = useState(period.days);
  const [invoice, setInvoice] = useState<RatedInvoice | null>(null);
  const [message, setMessage] = useState("Rate a usage period to create a draft invoice.");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    fetch("/v1/plans")
      .then((response) => (response.ok ? response.json() : Promise.reject(response)))
      .then((data: Plan[]) => {
        if (data.length > 0) setPlans(data);
      })
      .catch(() => {});
  }, []);

  async function rate() {
    setBusy(true);
    setMessage("Rating usage...");
    try {
      const response = await fetch("/v1/rated_invoices", {
        method: "POST",
        headers: {
          "content-type": "application/json",
          "Idempotency-Key": `web-rating-${Date.now()}`,
        },
        body: JSON.stringify({
          account_id: "customer_demo",
          plan_id: planId,
          period_start: period.start,
          period_end: period.end,
          usage_units: Number(usage),
          active_days: Number(activeDays),
        }),
      });
      const body = await response.json();
      if (!response.ok) {
        setMessage(body.error ?? "Rating failed");
        return;
      }
      setInvoice(body as RatedInvoice);
      setMessage("Draft invoice created. Posting it will write a double-entry ledger capture.");
    } catch {
      setMessage("API offline. Start the Go service to rate usage.");
    } finally {
      setBusy(false);
    }
  }

  async function capture() {
    if (!invoice) return;
    setBusy(true);
    setMessage("Posting invoice to the ledger...");
    try {
      const response = await fetch(`/v1/rated_invoices/${invoice.id}/capture`, { method: "POST" });
      const body = await response.json();
      if (!response.ok) {
        setMessage(body.error ?? "Capture failed");
        return;
      }
      setInvoice(body as RatedInvoice);
      setMessage(`Posted to the ledger as ${body.intent_id}.`);
    } catch {
      setMessage("API offline. Start the Go service to post the invoice.");
    } finally {
      setBusy(false);
    }
  }

  return (
    <section className="rating-studio" aria-labelledby="rating-title">
      <div className="rating-head">
        <div>
          <p className="rating-eyebrow">Devoli · Rating Studio</p>
          <h2 id="rating-title">Usage rating and billing</h2>
          <p className="rating-lede">
            Marginal tiers, included allowance, proration and 15% GST. The same Go service posts
            the invoice total to the double-entry ledger exactly once.
          </p>
        </div>
        <span className="rating-badge">NZD · GST 15%</span>
      </div>

      <div className="rating-grid">
        <div className="rating-controls">
          <label>
            Plan
            <select value={planId} onChange={(event) => setPlanId(event.target.value)}>
              {plans.map((plan) => (
                <option key={plan.id} value={plan.id}>
                  {plan.name}
                </option>
              ))}
            </select>
          </label>
          <label>
            Usage units
            <input
              type="number"
              min={0}
              value={usage}
              onChange={(event) => setUsage(Number(event.target.value))}
            />
          </label>
          <label>
            Active days
            <input
              type="number"
              min={1}
              max={period.days}
              value={activeDays}
              onChange={(event) => setActiveDays(Number(event.target.value))}
            />
          </label>
          <div className="rating-period">
            {period.start} → {period.end}
          </div>
          <button type="button" onClick={rate} disabled={busy}>
            Rate invoice
          </button>
        </div>

        <div className="rating-result">
          {invoice ? (
            <>
              <div className="rating-result-head">
                <div>
                  <p className="rating-label">Draft invoice</p>
                  <p className="rating-id">{invoice.id.slice(0, 18)}</p>
                </div>
                <span className={`rating-status ${invoice.status}`}>{invoice.status}</span>
              </div>
              <table className="rating-table">
                <thead>
                  <tr>
                    <th>Line</th>
                    <th>Units</th>
                    <th>Amount</th>
                  </tr>
                </thead>
                <tbody>
                  {invoice.lines.map((line, index) => (
                    <tr key={`${line.description}-${index}`}>
                      <td>{line.description}</td>
                      <td>{line.units}</td>
                      <td>{money(line.amount_cents, invoice.currency)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
              <dl className="rating-totals">
                <div><dt>Subtotal</dt><dd>{money(invoice.subtotal_cents, invoice.currency)}</dd></div>
                <div><dt>GST</dt><dd>{money(invoice.tax_cents, invoice.currency)}</dd></div>
                <div className="rating-total"><dt>Total</dt><dd>{money(invoice.total_cents, invoice.currency)}</dd></div>
              </dl>
              <button type="button" className="rating-secondary" onClick={capture} disabled={busy || invoice.status === "posted"}>
                {invoice.status === "posted" ? "Posted to ledger" : "Post to ledger"}
              </button>
            </>
          ) : (
            <div className="rating-empty">
              <p className="rating-label">No invoice yet</p>
              <p>Choose a plan, enter usage and rate the period.</p>
            </div>
          )}
        </div>
      </div>
      <p className="rating-message" role="status">{message}</p>
    </section>
  );
}
