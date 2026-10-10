import { isCurrency } from "@demo/money"
import { z } from "zod"

export const LineItem = z.object({
  description: z.string().min(1),
  quantity: z.number().int().positive(),
  unitPrice: z.object({
    currency: z.string().refine(isCurrency, "unknown currency"),
    minor: z.number().int().nonnegative(),
  }),
})

export type LineItem = z.infer<typeof LineItem>

export const parse = (input: unknown): LineItem => LineItem.parse(input)
