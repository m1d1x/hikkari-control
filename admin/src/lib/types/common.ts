export type TimeRange = '1h' | '24h' | '7d' | '30d' | '90d';

export interface MetricValue {
	value: number;
	previousValue: number;
}

export interface TimeSeriesPoint {
	timestamp: string;
	value: number;
}

export interface TimeSeries {
	label: string;
	data: TimeSeriesPoint[];
}

export interface PaginatedResponse<T> {
	data: T[];
	total: number;
	page: number;
	pageSize: number;
}
