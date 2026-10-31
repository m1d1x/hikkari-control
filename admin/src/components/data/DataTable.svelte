<script lang="ts">
	import type { Snippet } from 'svelte';
	import { icons } from '$components/icons';

	export interface Column {
		key: string;
		label: string;
		sortable?: boolean;
		align?: 'left' | 'center' | 'right';
		width?: string;
		mono?: boolean;
		render?: (value: any, row: any) => string;
	}

	interface Props {
		columns: Column[];
		rows: any[];
		/**
		 * Per-column cell renderers, keyed by column key. A column that needs
		 * markup (a badge, a link) gets a snippet here; everything else falls
		 * back to the column's `render` or the raw value. Snippets live in the
		 * caller's template, so they are passed as a prop rather than stored
		 * on the column object built in the caller's script.
		 */
		cells?: Record<string, Snippet<[any, any]>>;
		sortBy?: string;
		sortDir?: 'asc' | 'desc';
		page?: number;
		pageSize?: number;
		total?: number;
		onSort?: (key: string) => void;
		onPageChange?: (page: number) => void;
		onRowClick?: (row: any) => void;
		loading?: boolean;
	}

	let {
		columns,
		rows,
		cells,
		sortBy = '',
		sortDir = 'asc',
		page = 1,
		pageSize = 20,
		total,
		onSort,
		onPageChange,
		onRowClick,
		loading = false,
	}: Props = $props();

	let totalItems = $derived(total ?? rows.length);
	// pageSize 0 means "the caller shows everything at once" — no controls.
	let totalPages = $derived(pageSize > 0 ? Math.ceil(totalItems / pageSize) : 1);

	function handleSort(key: string) {
		onSort?.(key);
	}

	function handlePageChange(newPage: number) {
		if (newPage >= 1 && newPage <= totalPages) {
			onPageChange?.(newPage);
		}
	}

	function getCellValue(row: any, col: Column): string {
		const val = row[col.key];
		if (col.render) return col.render(val, row);
		if (val == null) return '—';
		return String(val);
	}
</script>

<div class="table-wrapper">
	<table>
		<thead>
			<tr>
				{#each columns as col}
					<th
						class:sortable={col.sortable}
						class:sorted={sortBy === col.key}
						style={col.width ? `width: ${col.width}` : ''}
						style:text-align={col.align ?? 'left'}
						onclick={() => col.sortable && handleSort(col.key)}
					>
						<span class="th-content">
							{col.label}
							{#if col.sortable}
								<span class="sort-icon">
									{#if sortBy === col.key}
										{@html sortDir === 'asc' ? icons.sortAsc : icons.sortDesc}
									{:else}
										{@html icons.sort}
									{/if}
								</span>
							{/if}
						</span>
					</th>
				{/each}
			</tr>
		</thead>
		<tbody>
			{#if loading}
				{#each Array(5) as _}
					<tr class="skeleton-row">
						{#each columns as _col}
							<td><div class="skeleton-cell"></div></td>
						{/each}
					</tr>
				{/each}
			{:else if rows.length === 0}
				<tr>
					<td colspan={columns.length} class="empty">No data</td>
				</tr>
			{:else}
				{#each rows as row}
					<tr class:clickable={!!onRowClick} onclick={() => onRowClick?.(row)}>
						{#each columns as col}
							<td class:mono={col.mono} style:text-align={col.align ?? 'left'}>
								{#if cells?.[col.key]}
									{@render cells[col.key](row[col.key], row)}
								{:else}
									{getCellValue(row, col)}
								{/if}
							</td>
						{/each}
					</tr>
				{/each}
			{/if}
		</tbody>
	</table>

	{#if totalPages > 1}
		<div class="pagination">
			<span class="info mono">
				{(page - 1) * pageSize + 1}–{Math.min(page * pageSize, totalItems)} of {totalItems}
			</span>
			<div class="page-controls">
				<button disabled={page <= 1} onclick={() => handlePageChange(page - 1)}>Prev</button>
				<span class="page-num mono">{page} / {totalPages}</span>
				<button disabled={page >= totalPages} onclick={() => handlePageChange(page + 1)}>Next</button>
			</div>
		</div>
	{/if}
</div>

<style>
	.table-wrapper {
		background: var(--surface);
		border: 1px solid var(--border-subtle);
		border-radius: var(--card-radius);
		overflow: hidden;
	}

	table {
		width: 100%;
	}

	thead {
		border-bottom: 1px solid var(--border);
	}

	th {
		font-size: 11px;
		font-weight: 600;
		text-transform: uppercase;
		letter-spacing: 0.04em;
		color: var(--text-muted);
		padding: 10px 16px;
		white-space: nowrap;
		user-select: none;
	}

	th.sortable {
		cursor: pointer;
	}

	th.sortable:hover {
		color: var(--text-secondary);
	}

	th.sorted {
		color: var(--text-primary);
	}

	.th-content {
		display: inline-flex;
		align-items: center;
		gap: 4px;
	}

	.sort-icon {
		display: inline-flex;
		opacity: 0.5;
	}

	th.sorted .sort-icon {
		opacity: 1;
	}

	td {
		font-size: 13px;
		padding: 10px 16px;
		color: var(--text-primary);
		border-bottom: 1px solid var(--border-subtle);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
		max-width: 300px;
	}

	tr.clickable {
		cursor: pointer;
	}

	tr.clickable:hover td {
		background: var(--surface-hover);
	}

	.empty {
		text-align: center;
		color: var(--text-muted);
		padding: 40px 16px;
	}

	.pagination {
		display: flex;
		align-items: center;
		justify-content: space-between;
		padding: 10px 16px;
		border-top: 1px solid var(--border-subtle);
	}

	.info {
		font-size: 12px;
		color: var(--text-muted);
	}

	.page-controls {
		display: flex;
		align-items: center;
		gap: 8px;
	}

	.page-controls button {
		font-size: 12px;
		font-weight: 500;
		color: var(--text-secondary);
		padding: 4px 10px;
		border-radius: 4px;
		border: 1px solid var(--border);
		transition: all var(--transition-fast);
	}

	.page-controls button:hover:not(:disabled) {
		color: var(--text-primary);
		border-color: var(--text-muted);
	}

	.page-controls button:disabled {
		opacity: 0.3;
		cursor: not-allowed;
	}

	.page-num {
		font-size: 12px;
		color: var(--text-secondary);
	}

	/* Skeleton */
	.skeleton-row td {
		padding: 12px 16px;
	}

	.skeleton-cell {
		height: 14px;
		width: 70%;
		border-radius: 4px;
		background: var(--surface-hover);
		animation: pulse 1.5s ease-in-out infinite;
	}

	@keyframes pulse {
		0%,
		100% {
			opacity: 1;
		}
		50% {
			opacity: 0.5;
		}
	}
</style>
