/**
 * Error types shared by the extension.
 *
 * ADR 0001: while remote mode is active, project-facing tools must fail
 * closed. Loss of the daemon or workspace must produce a visible error and
 * must never cause silent fallback to the developer machine.
 */

/** The workspace is not usable (disconnected, misconfigured, or missing). */
export class ScrapsUnavailableError extends Error {
	constructor(message: string) {
		super(message);
		this.name = "ScrapsUnavailableError";
	}
}

/** scrapd answered with a non-2xx response. */
export class ScrapdApiError extends Error {
	readonly status: number;

	constructor(status: number, message: string) {
		super(message.length > 0 ? message : `scrapd request failed with status ${status}`);
		this.name = "ScrapdApiError";
		this.status = status;
	}

	/** Build an error from a fetch Response, preferring a JSON error body. */
	static async from(response: Response): Promise<ScrapdApiError> {
		let message = "";
		try {
			const body = (await response.json()) as { error?: string; message?: string };
			message = body.error ?? body.message ?? "";
		} catch {
			// Non-JSON error body; fall back to the status code.
		}
		return new ScrapdApiError(response.status, message);
	}
}
