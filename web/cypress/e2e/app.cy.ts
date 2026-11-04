describe("ledger control plane", () => {
  it("renders the balance and the posting chart", () => {
    cy.visit("/");
    cy.contains("h1", "Ledger Control Plane");
    cy.get('[data-testid="balance"]').invoke("text").should("match", /\$/);
    cy.get('[data-testid="posting-count"]').should("contain.text", "6");
    cy.get("svg.chart rect").should("have.length.greaterThan", 0);
  });

  it("shows the API surface", () => {
    cy.visit("/");
    cy.contains("POST /openbanking/v1/payments");
    cy.contains("GET /metrics");
  });
});
