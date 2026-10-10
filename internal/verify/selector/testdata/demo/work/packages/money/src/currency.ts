export type Currency = "USD" | "EUR" | "JPY"

const minorUnits: Record<Currency, number> = { USD: 2, EUR: 2, JPY: 0 }

export const minorUnitsOf = (currency: Currency): number => minorUnits[currency]

export const isCurrency = (value: string): value is Currency => value in minorUnits
