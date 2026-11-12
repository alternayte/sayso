// formatPrice returns cents as a price text.
export function formatPrice(cents) {
  return "$" + (cents / 100).toFixed(2);
}
