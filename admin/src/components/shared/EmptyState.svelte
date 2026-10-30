<script lang="ts">
	interface Props {
		message?: string;
	}

	let { message = 'No data available' }: Props = $props();
</script>

<div class="empty-state">
	<p>{message}</p>
</div>

<style>
	.empty-state {
		display: flex;
		align-items: center;
		justify-content: center;
		padding: 48px 24px;
		color: var(--text-muted);
		font-size: 14px;
	}
</style>
