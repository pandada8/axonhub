package biz

import (
	"github.com/shopspring/decimal"

	"github.com/looplj/axonhub/internal/objects"
	"github.com/looplj/axonhub/llm"
)

func unitsInMillionTokens(units int64) decimal.Decimal {
	if units <= 0 {
		return decimal.Zero
	}

	return decimal.NewFromInt(units).Div(decimal.NewFromInt(1_000_000))
}

func computeItemSubtotal(quantity int64, pricing objects.Pricing) (objects.CostItem, decimal.Decimal) {
	item := objects.CostItem{
		Quantity: quantity,
	}

	switch pricing.Mode {
	case objects.PricingModeFlatFee:
		if pricing.FlatFee != nil {
			item.Subtotal = *pricing.FlatFee

			return item, *pricing.FlatFee
		}

		return item, decimal.Zero
	case objects.PricingModeUsagePerUnit:
		if pricing.UsagePerUnit != nil {
			sub := pricing.UsagePerUnit.Mul(unitsInMillionTokens(quantity))
			item.Subtotal = sub

			return item, sub
		}

		return item, decimal.Zero
	case objects.PricingModeTiered:
		if pricing.UsageTiered != nil {
			// Volume tiered pricing: find the tier that the quantity falls into
			// and apply that tier's price to the entire quantity.
			// Tiers are sorted by UpTo, with the last tier having nil UpTo (unlimited).
			var selectedPrice decimal.Decimal

			for _, tier := range pricing.UsageTiered.Tiers {
				selectedPrice = tier.PricePerUnit

				if tier.UpTo != nil && quantity > *tier.UpTo {
					// Quantity exceeds this tier, continue to next tier
					continue
				}

				// Quantity falls within this tier (or this is the last unlimited tier)
				break
			}

			sub := selectedPrice.Mul(unitsInMillionTokens(quantity))
			item.Subtotal = sub

			return item, sub
		}

		return item, decimal.Zero
	default:
		return item, decimal.Zero
	}
}

// ComputeUsageCost calculates total cost and cost items breakdown for the given usage and model price.
func ComputeUsageCost(usage *llm.Usage, price objects.ModelPrice) ([]objects.CostItem, decimal.Decimal) {
	var items []objects.CostItem

	total := decimal.Zero

	for _, it := range price.Items {
		var quantity int64

		switch it.ItemCode {
		case objects.PriceItemCodeUsage:
			// Exclude cached tokens from input token cost calculation
			// PromptTokens includes all tokens, so we subtract:
			// - CachedTokens (read from cache)
			// - WriteCachedTokens (write to cache)
			// These are billed separately at different rates
			quantity = usage.PromptTokens
			if usage.PromptTokensDetails != nil {
				quantity -= usage.PromptTokensDetails.CachedTokens
				quantity -= usage.PromptTokensDetails.WriteCachedTokens
			}
			// Clamp to non-negative to handle provider bugs or partial data
			if quantity < 0 {
				quantity = 0
			}
		case objects.PriceItemCodeCompletion:
			quantity = usage.CompletionTokens
		case objects.PriceItemCodePromptCachedToken:
			if usage.PromptTokensDetails != nil {
				quantity = usage.PromptTokensDetails.CachedTokens
			}
		case objects.PriceItemCodeWriteCachedTokens:
			// Handle write cached tokens with variant support
			if usage.PromptTokensDetails != nil {
				// Check if we have 5m/1h cache variants to calculate separately
				if usage.PromptTokensDetails.WriteCached5MinTokens > 0 || usage.PromptTokensDetails.WriteCached1HourTokens > 0 {
					// Process 5m variant if present
					if usage.PromptTokensDetails.WriteCached5MinTokens > 0 {
						pricing := it.FindPromptWriteCacheVariantPricing(objects.PromptWriteCacheVariantCode5Min)
						item, sub := computeItemSubtotal(usage.PromptTokensDetails.WriteCached5MinTokens, pricing)
						item.ItemCode = it.ItemCode
						item.PromptWriteCacheVariantCode = objects.PromptWriteCacheVariantCode5Min
						items = append(items, item)
						total = total.Add(sub)
					}

					// Process 1h variant if present
					if usage.PromptTokensDetails.WriteCached1HourTokens > 0 {
						pricing := it.FindPromptWriteCacheVariantPricing(objects.PromptWriteCacheVariantCode1Hour)
						item, sub := computeItemSubtotal(usage.PromptTokensDetails.WriteCached1HourTokens, pricing)
						item.ItemCode = it.ItemCode
						item.PromptWriteCacheVariantCode = objects.PromptWriteCacheVariantCode1Hour
						items = append(items, item)
						total = total.Add(sub)
					}

					// Skip the shared pricing calculation
					continue
				}

				// Fallback to shared WriteCachedTokens if no variants present
				quantity = usage.PromptTokensDetails.WriteCachedTokens
			}
		default:
			quantity = 0
		}

		item, sub := computeItemSubtotal(quantity, it.Pricing)
		item.ItemCode = it.ItemCode
		items = append(items, item)
		total = total.Add(sub)
	}

	return items, total
}
