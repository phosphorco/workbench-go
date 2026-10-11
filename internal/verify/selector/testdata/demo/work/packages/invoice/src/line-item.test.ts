import { expect, test } from "bun:test"
import { parse } from "./line-item.ts"

test("accepts a well-formed line item", () => {
  const item = parse({ description: "Widget", quantity: 2, unitPrice: { currency: "USD", minor: 999 } })
  expect(item.quantity).toBe(2)
})

test("rejects a zero quantity", () => {
  expect(() => parse({ description: "Widget", quantity: 0, unitPrice: { currency: "USD", minor: 1 } })).toThrow()
})

test("rejects an unknown currency", () => {
  expect(() => parse({ description: "Widget", quantity: 1, unitPrice: { currency: "XYZ", minor: 1 } })).toThrow()
})
