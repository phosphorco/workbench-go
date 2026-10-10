import { format } from "@demo/money"
import { expect, test } from "bun:test"
import { parse } from "./line-item.ts"
import { total } from "./total.ts"

test("totals line items", () => {
  const items = [
    parse({ description: "Widget", quantity: 2, unitPrice: { currency: "EUR", minor: 250 } }),
    parse({ description: "Gadget", quantity: 1, unitPrice: { currency: "EUR", minor: 1000 } }),
  ]
  expect(format(total("EUR", items))).toBe("15.00 EUR")
})

test("an empty invoice totals zero", () => {
  expect(format(total("USD", []))).toBe("0.00 USD")
})
