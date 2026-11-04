import { configureStore, createSlice, type PayloadAction } from "@reduxjs/toolkit";

export type Posting = {
  intentId: string;
  accountId: string;
  amountCents: number;
  currency: string;
};

type LedgerState = {
  balanceCents: number;
  postings: Posting[];
  status: "idle" | "loading" | "ready" | "offline";
};

const samplePostings: Posting[] = [
  { intentId: "pi_1", accountId: "customer_demo", amountCents: -400, currency: "USD" },
  { intentId: "pi_1", accountId: "merchant_settlement", amountCents: 400, currency: "USD" },
  { intentId: "pi_2", accountId: "customer_demo", amountCents: -250, currency: "USD" },
  { intentId: "pi_2", accountId: "merchant_settlement", amountCents: 250, currency: "USD" },
  { intentId: "pi_3", accountId: "customer_demo", amountCents: -125, currency: "USD" },
  { intentId: "pi_3", accountId: "merchant_settlement", amountCents: 125, currency: "USD" },
];

const initialState: LedgerState = {
  balanceCents: 10000,
  postings: samplePostings,
  status: "idle",
};

const ledgerSlice = createSlice({
  name: "ledger",
  initialState,
  reducers: {
    setBalance(state, action: PayloadAction<number>) {
      state.balanceCents = action.payload;
      state.status = "ready";
    },
    setPostings(state, action: PayloadAction<Posting[]>) {
      state.postings = action.payload;
    },
    setOffline(state) {
      state.status = "offline";
    },
    setLoading(state) {
      state.status = "loading";
    },
  },
});

export const { setBalance, setPostings, setOffline, setLoading } = ledgerSlice.actions;

export const store = configureStore({
  reducer: { ledger: ledgerSlice.reducer },
});

export type RootState = ReturnType<typeof store.getState>;
export type AppDispatch = typeof store.dispatch;
