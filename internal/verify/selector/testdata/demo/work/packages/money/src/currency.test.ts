import { expect, test } from "bun:test"
import { isCurrency, minorUnitsOf } from "./currency.ts"

test("yen has no minor units", () => {
  expect(minorUnitsOf("JPY")).toBe(0)
})

test("unknown codes are not currencies", () => {
  expect(isCurrency("XYZ")).toBe(false)
  expect(isCurrency("EUR")).toBe(true)
})
