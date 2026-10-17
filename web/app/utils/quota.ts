// The whole units a size in bytes holds, as a form shows it.
export function sizeInUnits(bytes: bigint, unit: bigint): number {
  return Number(bytes / unit)
}

// The bytes a form saves for a size shown in units: the original bytes while
// the shown value is unchanged.
export function sizeInBytes(value: number, unit: bigint, original = 0n): bigint {
  return value === sizeInUnits(original, unit) ? original : BigInt(value) * unit
}
