// fullBleedPanel is the UDashboardPanel ui for a full-page table.
export const fullBleedPanel = { body: 'p-0 sm:p-0 gap-0 sm:gap-0 overflow-hidden' }

// denseTable is the UTable ui: bordered cells, sticky header.
export const denseTable = {
  root: 'min-h-0 flex-1',
  th: 'border-b border-default border-r border-muted last:border-r-0',
  td: 'border-r border-muted last:border-r-0'
}

// cardTable is the UTable ui for a table in a card: denseTable's bordered cells, as tall as its rows.
export const cardTable = {
  th: denseTable.th,
  td: denseTable.td
}

// paginate returns one page of rows.
export function paginate<T>(rows: T[], page: number, pageSize: number): T[] {
  return rows.slice((page - 1) * pageSize, page * pageSize)
}
