import { type Currency, type Money, add, make, times } from "@demo/money"
import type { LineItem } from "./line-item.ts"

export const total = (currency: Currency, items: ReadonlyArray<LineItem>): Money =>
  items.reduce(
    (sum, item) => add(sum, times(make(item.unitPrice.currency as Currency, item.unitPrice.minor), item.quantity)),
    make(currency, 0),
  )
