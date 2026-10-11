import { type Currency, minorUnitsOf } from "./currency.ts"

export interface Money {
  readonly currency: Currency
  readonly minor: number
}

export const make = (currency: Currency, minor: number): Money => ({ currency, minor })

export const add = (a: Money, b: Money): Money => {
  if (a.currency !== b.currency) throw new Error(`cannot add ${a.currency} to ${b.currency}`)
  return make(a.currency, a.minor + b.minor)
}

export const times = (m: Money, quantity: number): Money => make(m.currency, m.minor * quantity)

export const format = (m: Money): string => {
  const digits = minorUnitsOf(m.currency)
  const major = (m.minor / 10 ** digits).toFixed(digits)
  return `${major} ${m.currency}`
}
