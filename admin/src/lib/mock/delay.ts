export function withDelay<T>(data: T, minMs = 80, maxMs = 250): Promise<T> {
	const delay = Math.floor(Math.random() * (maxMs - minMs)) + minMs;
	return new Promise((resolve) => setTimeout(() => resolve(data), delay));
}
