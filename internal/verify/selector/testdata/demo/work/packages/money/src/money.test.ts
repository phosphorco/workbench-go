import { expect, test } from "bun:test"
import { add, format, make, times } from "./money.ts"

test("adds amounts in one currency", () => {
  expect(add(make("USD", 150), make("USD", 275))).toEqual(make("USD", 425))
})

test("refuses to add different currencies", () => {
  expect(() => add(make("USD", 1), make("EUR", 1))).toThrow()
})

test("multiplies by a quantity", () => {
  expect(times(make("EUR", 199), 3)).toEqual(make("EUR", 597))
})

test("formats with the currency's minor units", () => {
  expect(format(make("USD", 1234))).toBe("12.34 USD")
  expect(format(make("JPY", 500))).toBe("500 JPY")
})
