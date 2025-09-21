class RAGChatApp {
	constructor() {
		this.apiUrl = this.getApiUrl();
		this.currentSessionId = null;
		this.sessions = [];

		// DOM elements
		this.sidebar = document.getElementById("sidebar");
		this.sidebarToggle = document.getElementById("sidebarToggle");
		this.chatMessages = document.getElementById("chatMessages");
		this.messageInput = document.getElementById("messageInput");
		this.sendButton = document.getElementById("sendButton");
		this.statusDot = document.getElementById("statusDot");
		this.statusText = document.getElementById("statusText");
		this.charCount = document.getElementById("charCount");
		this.loadingOverlay = document.getElementById("loadingOverlay");
		this.newChatBtn = document.getElementById("newChatBtn");
		this.sessionsList = document.getElementById("sessionsList");
		this.uploadArea = document.getElementById("uploadArea");
		this.fileInput = document.getElementById("fileInput");
		this.uploadProgress = document.getElementById("uploadProgress");
		this.progressFill = document.getElementById("progressFill");
		this.progressText = document.getElementById("progressText");
		this.chatTitle = document.getElementById("chatTitle");
		this.chatSubtitle = document.getElementById("chatSubtitle");

		this.initializeEventListeners();
		this.checkConnection();
		this.loadSessions();
		this.initializeChatTitleEditing();
		this.initializeSourceSearch();
		this.initializeFlushButton();
	}

	getApiUrl() {
		const currentUrl = window.location.origin;
		console.log("Detected API URL:", currentUrl);
		return currentUrl;
	}

	initializeEventListeners() {
		// Send button click
		this.sendButton.addEventListener("click", () => this.sendMessage());

		// Enter key to send (Shift+Enter for new line)
		this.messageInput.addEventListener("keydown", (e) => {
			if (e.key === "Enter" && !e.shiftKey) {
				e.preventDefault();
				this.sendMessage();
			}
		});

		// Auto-resize textarea
		this.messageInput.addEventListener("input", () => {
			this.autoResizeTextarea();
			this.updateCharCount();
			this.updateSendButton();
		});

		// Sidebar toggle
		this.sidebarToggle.addEventListener("click", () => this.toggleSidebar());

		// New chat button
		this.newChatBtn.addEventListener("click", () => this.createNewSession());

		// File upload
		this.uploadArea.addEventListener("click", () => this.fileInput.click());
		this.fileInput.addEventListener("change", (e) => this.handleFileUpload(e));

		// Drag and drop
		this.uploadArea.addEventListener("dragover", (e) => {
			e.preventDefault();
			this.uploadArea.style.borderColor = "#3498db";
			this.uploadArea.style.background = "rgba(52, 152, 219, 0.1)";
		});

		this.uploadArea.addEventListener("dragleave", (e) => {
			e.preventDefault();
			this.uploadArea.style.borderColor = "#34495e";
			this.uploadArea.style.background = "transparent";
		});

		this.uploadArea.addEventListener("drop", (e) => {
			e.preventDefault();
			this.uploadArea.style.borderColor = "#34495e";
			this.uploadArea.style.background = "transparent";
			this.handleFileUpload(e);
		});

		// Check connection periodically
		setInterval(() => this.checkConnection(), 30000);
	}

	initializeChatTitleEditing() {
		// Handle chat title editing
		this.chatTitle.addEventListener("blur", () => {
			this.saveChatTitle();
		});

		this.chatTitle.addEventListener("keydown", (e) => {
			if (e.key === "Enter") {
				e.preventDefault();
				this.chatTitle.blur();
			}
			if (e.key === "Escape") {
				e.preventDefault();
				this.chatTitle.textContent =
					this.currentSessionTitle || "RAG Assistant";
				this.chatTitle.blur();
			}
		});

		// Prevent newlines in the title
		this.chatTitle.addEventListener("input", (e) => {
			if (e.inputType === "insertLineBreak") {
				e.preventDefault();
			}
		});
	}

	async saveChatTitle() {
		if (!this.currentSessionId) return;

		const newTitle = this.chatTitle.textContent.trim();
		if (!newTitle || newTitle === this.currentSessionTitle) return;

		try {
			const response = await fetch(
				`${this.apiUrl}/sessions/${this.currentSessionId}`,
				{
					method: "PUT",
					headers: {
						"Content-Type": "application/json",
					},
					body: JSON.stringify({ title: newTitle }),
				}
			);

			if (response.ok) {
				this.currentSessionTitle = newTitle;
				// Update the session in the sidebar
				this.loadSessions();
			} else {
				// Revert on error
				this.chatTitle.textContent =
					this.currentSessionTitle || "RAG Assistant";
			}
		} catch (error) {
			console.error("Failed to update chat title:", error);
			// Revert on error
			this.chatTitle.textContent = this.currentSessionTitle || "RAG Assistant";
		}
	}

	toggleSidebar() {
		this.sidebar.classList.toggle("open");
	}

	async checkConnection() {
		try {
			const response = await fetch(`${this.apiUrl}/health`);
			const data = await response.json();

			if (data.status === "healthy") {
				this.updateStatus("connected", "Connected");
			} else {
				this.updateStatus("disconnected", "Service Unhealthy");
			}
		} catch (error) {
			console.warn("Connection check failed:", error);
			this.updateStatus("disconnected", "Disconnected");
		}
	}

	updateStatus(status, text) {
		this.statusDot.className = `status-dot ${status}`;
		this.statusText.textContent = text;
	}

	async loadSessions() {
		try {
			const response = await fetch(`${this.apiUrl}/sessions`);
			const sessions = await response.json();
			this.sessions = sessions;
			this.renderSessions();
		} catch (error) {
			console.error("Failed to load sessions:", error);
		}
	}

	renderSessions() {
		this.sessionsList.innerHTML = "";

		if (this.sessions.length === 0) {
			this.sessionsList.innerHTML =
				'<p style="color: #7f8c8d; text-align: center; font-size: 0.9rem;">No chat sessions yet</p>';
			return;
		}

		this.sessions.forEach((session) => {
			const sessionElement = document.createElement("div");
			sessionElement.className = "session-item";
			if (session.id === this.currentSessionId) {
				sessionElement.classList.add("active");
			}

			const time = new Date(session.created_at).toLocaleDateString();

			sessionElement.innerHTML = `
				<div class="session-title">${session.title}</div>
				<div class="session-time">${time}</div>
				<div class="session-actions">
					<button class="session-delete-btn" onclick="event.stopPropagation(); app.deleteSession('${session.id}')">
						<i class="fas fa-trash"></i>
					</button>
				</div>
			`;

			sessionElement.addEventListener("click", () =>
				this.loadSession(session.id)
			);
			this.sessionsList.appendChild(sessionElement);
		});
	}

	async createNewSession() {
		try {
			const response = await fetch(`${this.apiUrl}/sessions`, {
				method: "POST",
				headers: {
					"Content-Type": "application/json",
				},
				body: JSON.stringify({ title: "New Chat" }),
			});

			const session = await response.json();
			this.sessions.unshift(session);
			this.renderSessions();
			this.loadSession(session.id);
		} catch (error) {
			console.error("Failed to create session:", error);
			alert("Failed to create new chat session");
		}
	}

	async loadSession(sessionId) {
		try {
			const response = await fetch(`${this.apiUrl}/sessions/${sessionId}`);
			const data = await response.json();

			this.currentSessionId = sessionId;
			this.currentSessionTitle = data.session.title;
			this.chatTitle.textContent = data.session.title;
			this.chatSubtitle.textContent = "Ask questions about your documents";

			// Clear current messages
			this.chatMessages.innerHTML = "";

			// Load session messages
			if (data.messages && data.messages.length > 0) {
				data.messages.forEach((message) => {
					// Parse sources if it's a JSON string
					let sources = [];
					if (message.sources) {
						if (typeof message.sources === "string") {
							try {
								sources = JSON.parse(message.sources);
							} catch (e) {
								console.warn("Failed to parse sources:", e);
								sources = [];
							}
						} else if (Array.isArray(message.sources)) {
							sources = message.sources;
						}
					}
					this.addMessageToUI(message.content, message.role, sources);
				});
			} else {
				this.showWelcomeMessage();
			}

			// Update active session in sidebar
			this.renderSessions();
		} catch (error) {
			console.error("Failed to load session:", error);
			alert("Failed to load chat session");
		}
	}

	async deleteSession(sessionId) {
		if (!confirm("Are you sure you want to delete this chat session?")) {
			return;
		}

		try {
			await fetch(`${this.apiUrl}/sessions/${sessionId}`, {
				method: "DELETE",
			});

			this.sessions = this.sessions.filter((s) => s.id !== sessionId);
			this.renderSessions();

			if (this.currentSessionId === sessionId) {
				this.currentSessionId = null;
				this.chatTitle.textContent = "RAG Assistant";
				this.chatSubtitle.textContent = "Ask questions about your documents";
				this.showWelcomeMessage();
			}
		} catch (error) {
			console.error("Failed to delete session:", error);
			alert("Failed to delete chat session");
		}
	}

	showWelcomeMessage() {
		this.chatMessages.innerHTML = `
			<div class="welcome-message">
				<div class="welcome-icon">
					<i class="fas fa-robot"></i>
				</div>
				<div class="welcome-content">
					<h3>Welcome to RAG Chat!</h3>
					<p>Upload PDF documents and start asking questions. I'll help you find information from your documents.</p>
					<div class="welcome-actions">
						<button class="welcome-btn" onclick="app.uploadArea.click()">
							<i class="fas fa-upload"></i>
							Upload PDFs
						</button>
						<button class="welcome-btn" onclick="app.createNewSession()">
							<i class="fas fa-comments"></i>
							Start New Chat
						</button>
					</div>
				</div>
			</div>
		`;
	}

	async sendMessage() {
		if (!this.currentSessionId) {
			await this.createNewSession();
		}

		const message = this.messageInput.value.trim();
		if (!message) return;

		// Add user message to UI
		this.addMessageToUI(message, "user");

		// Clear input
		this.messageInput.value = "";
		this.autoResizeTextarea();
		this.updateCharCount();
		this.updateSendButton();

		// Show loading
		this.showLoading();

		try {
			const response = await fetch(
				`${this.apiUrl}/sessions/${this.currentSessionId}/chat`,
				{
					method: "POST",
					headers: {
						"Content-Type": "application/json",
					},
					body: JSON.stringify({ message: message }),
				}
			);

			if (!response.ok) {
				throw new Error(`HTTP error! status: ${response.status}`);
			}

			const data = await response.json();

			// Add bot response to UI
			this.addMessageToUI(data.answer, "assistant", data.sources);
		} catch (error) {
			console.error("Error sending message:", error);
			this.addMessageToUI(
				"Sorry, I encountered an error. Please try again.",
				"assistant",
				[],
				true
			);
		} finally {
			this.hideLoading();
		}
	}

	addMessageToUI(text, sender, sources = [], isError = false) {
		// Remove welcome message if it exists
		const welcomeMessage = this.chatMessages.querySelector(".welcome-message");
		if (welcomeMessage) {
			welcomeMessage.remove();
		}

		const messageDiv = document.createElement("div");
		messageDiv.className = `message ${sender}-message`;

		const avatar = document.createElement("div");
		avatar.className = "message-avatar";
		avatar.innerHTML =
			sender === "user"
				? '<i class="fas fa-user"></i>'
				: '<i class="fas fa-robot"></i>';

		const content = document.createElement("div");
		content.className = "message-content";

		const messageText = document.createElement("div");
		messageText.className = "message-text";
		messageText.innerHTML = this.formatMessage(text);

		const messageTime = document.createElement("div");
		messageTime.className = "message-time";
		messageTime.textContent = this.formatTime(new Date());

		content.appendChild(messageText);
		content.appendChild(messageTime);

		// Add sources if available
		if (sources && Array.isArray(sources) && sources.length > 0) {
			const sourcesDiv = document.createElement("div");
			sourcesDiv.className = "message-sources";
			sourcesDiv.innerHTML = `
				<h4>Sources (${sources.length}):</h4>
				${sources
					.map(
						(source, index) => `
					<div class="source-item">
						<div class="source-info">
							<span class="source-name">${this.getFilenameFromSource(source)}</span>
							<span class="source-rank">#${index + 1}</span>
						</div>
						<a href="${this.apiUrl}/files/${this.getDocumentIdFromSource(
							source
						)}/${this.getFilenameFromSource(
							source
						)}" target="_blank" class="download-btn" onclick="console.log('Downloading:', '${this.getDocumentIdFromSource(
							source
						)}', '${this.getFilenameFromSource(source)}')">
							<i class="fas fa-download"></i> Download
						</a>
					</div>
				`
					)
					.join("")}
			`;
			content.appendChild(sourcesDiv);
		}

		messageDiv.appendChild(avatar);
		messageDiv.appendChild(content);

		this.chatMessages.appendChild(messageDiv);

		// Scroll to bottom
		this.scrollToBottom();

		// Add error styling if needed
		if (isError) {
			messageText.style.color = "#e74c3c";
			messageText.style.fontStyle = "italic";
		}
	}

	formatMessage(text) {
		// Convert markdown-like formatting to HTML
		return text
			.replace(/\*\*(.*?)\*\*/g, "<strong>$1</strong>")
			.replace(/\*(.*?)\*/g, "<em>$1</em>")
			.replace(/\n/g, "<br>");
	}

	getDocumentIdFromSource(source) {
		// Check if source contains document ID (format: "doc_id|filename")
		if (source && source.includes("|")) {
			return source.split("|")[0];
		}
		// Fallback: generate document ID from filename
		return "doc_" + (source || "").replace(/[^a-zA-Z0-9]/g, "_");
	}

	getFilenameFromSource(source) {
		// Check if source contains document ID (format: "doc_id|filename")
		if (source && source.includes("|")) {
			return source.split("|")[1];
		}
		// Fallback: use source as filename
		return source || "unknown.pdf";
	}

	initializeSourceSearch() {
		const searchInput = document.getElementById("sourceSearchInput");
		const searchBtn = document.getElementById("searchSourcesBtn");

		searchBtn.addEventListener("click", () => {
			this.searchSources();
		});

		searchInput.addEventListener("keypress", (e) => {
			if (e.key === "Enter") {
				this.searchSources();
			}
		});
	}

	async searchSources() {
		const searchInput = document.getElementById("sourceSearchInput");
		const query = searchInput.value.trim();

		if (!query) {
			alert("Please enter a search query");
			return;
		}

		try {
			const response = await fetch(`${this.apiUrl}/search-sources`, {
				method: "POST",
				headers: {
					"Content-Type": "application/json",
				},
				body: JSON.stringify({ query }),
			});

			if (!response.ok) {
				throw new Error("Search failed");
			}

			const data = await response.json();
			this.displaySourceSearchResults(data);
		} catch (error) {
			console.error("Source search failed:", error);
			this.addMessageToUI(
				"Failed to search sources. Please try again.",
				"assistant",
				[],
				true
			);
		}
	}

	displaySourceSearchResults(data) {
		const message = `
			<div class="source-search-results">
				<div class="search-results-header">
					<h4>Found ${data.count} source(s) containing "${data.query}":</h4>
					<button class="download-all-btn" onclick="window.downloadAllSearchResults()">
						<i class="fas fa-download"></i> Download All
					</button>
				</div>
				${data.sources
					.map(
						(source, index) => `
					<div class="source-result">
						<div class="source-header">
							<div class="source-title">
								<span class="source-rank">#${index + 1}</span>
								<strong>${source.filename}</strong>
							</div>
							<span class="relevance-score">Relevance: ${source.relevance_score.toFixed(
								2
							)}</span>
						</div>
						<div class="source-snippet">${source.snippet}</div>
						<div class="source-meta">
							<span>Chunks: ${source.chunk_count}</span>
							<a href="${this.apiUrl}/files/${source.document_id}/${
							source.filename
						}" target="_blank" class="download-btn" onclick="console.log('Downloading from search:', '${
							source.document_id
						}', '${source.filename}')">
								<i class="fas fa-download"></i> Download
							</a>
						</div>
					</div>
				`
					)
					.join("")}
			</div>
		`;

		this.addMessageToUI(message, "assistant", [], false);

		// Store search results for download all functionality
		window.currentSearchResults = data.sources;
	}

	initializeFlushButton() {
		const flushBtn = document.getElementById("flushBtn");

		flushBtn.addEventListener("click", () => {
			this.showFlushConfirmation();
		});
	}

	showFlushConfirmation() {
		const confirmed = confirm(
			"⚠️ WARNING: This will permanently delete ALL data including:\n\n" +
				"• All chat sessions and messages\n" +
				"• All uploaded PDF files\n" +
				"• All document chunks and embeddings\n" +
				"• All MinIO storage data\n\n" +
				"This action cannot be undone!\n\n" +
				"Are you sure you want to continue?"
		);

		if (confirmed) {
			this.flushAllData();
		}
	}

	async flushAllData() {
		try {
			// Show loading state
			const flushBtn = document.getElementById("flushBtn");
			const originalHTML = flushBtn.innerHTML;
			flushBtn.innerHTML = '<i class="fas fa-spinner fa-spin"></i>';
			flushBtn.disabled = true;

			const response = await fetch(`${this.apiUrl}/flush`, {
				method: "DELETE",
				headers: {
					"Content-Type": "application/json",
				},
			});

			if (!response.ok) {
				throw new Error("Flush failed");
			}

			const data = await response.json();

			// Clear the UI
			this.chatMessages.innerHTML = `
				<div class="welcome-message">
					<div class="welcome-icon">
						<i class="fas fa-robot"></i>
					</div>
					<h2>Welcome to RAG Assistant</h2>
					<p>All data has been cleared. Upload some PDF files to get started!</p>
				</div>
			`;

			// Clear current session
			this.currentSessionId = null;
			this.currentSessionTitle = null;
			this.chatTitle.textContent = "RAG Assistant";
			this.chatSubtitle.textContent = "Ask questions about your documents";

			// Reload sessions
			this.loadSessions();

			// Show success message
			this.addMessageToUI(
				"✅ All data has been successfully cleared! The system is now clean and ready for new documents.",
				"assistant",
				[],
				false
			);
		} catch (error) {
			console.error("Flush failed:", error);
			this.addMessageToUI(
				"❌ Failed to clear data. Please try again.",
				"assistant",
				[],
				true
			);
		} finally {
			// Restore button state
			const flushBtn = document.getElementById("flushBtn");
			flushBtn.innerHTML = '<i class="fas fa-trash-alt"></i>';
			flushBtn.disabled = false;
		}
	}

	async handleFileUpload(event) {
		const files = event.target.files || event.dataTransfer?.files;
		if (!files || files.length === 0) return;

		const formData = new FormData();
		Array.from(files).forEach((file) => {
			if (file.type === "application/pdf") {
				formData.append("files", file);
			}
		});

		if (formData.getAll("files").length === 0) {
			alert("Please select PDF files only");
			return;
		}

		this.showUploadProgress();

		try {
			const response = await fetch(`${this.apiUrl}/upload`, {
				method: "POST",
				body: formData,
			});

			const result = await response.json();
			this.hideUploadProgress();

			if (result.results) {
				const successCount = result.results.filter(
					(r) => r.status === "success"
				).length;
				const errorCount = result.results.filter(
					(r) => r.status === "error"
				).length;

				if (successCount > 0) {
					alert(`Successfully uploaded ${successCount} PDF(s)`);
				}
				if (errorCount > 0) {
					alert(
						`Failed to upload ${errorCount} PDF(s). Check console for details.`
					);
					console.error(
						"Upload errors:",
						result.results.filter((r) => r.status === "error")
					);
				}
			}
		} catch (error) {
			console.error("Upload failed:", error);
			alert("Upload failed. Please try again.");
			this.hideUploadProgress();
		}
	}

	showUploadProgress() {
		this.uploadProgress.style.display = "block";
		this.progressFill.style.width = "0%";
		this.progressText.textContent = "Uploading...";

		// Simulate progress
		let progress = 0;
		const interval = setInterval(() => {
			progress += 10;
			this.progressFill.style.width = progress + "%";
			if (progress >= 90) {
				clearInterval(interval);
			}
		}, 100);
	}

	hideUploadProgress() {
		this.progressFill.style.width = "100%";
		setTimeout(() => {
			this.uploadProgress.style.display = "none";
		}, 500);
	}

	autoResizeTextarea() {
		this.messageInput.style.height = "auto";
		this.messageInput.style.height =
			Math.min(this.messageInput.scrollHeight, 120) + "px";
	}

	updateCharCount() {
		const count = this.messageInput.value.length;
		this.charCount.textContent = `${count}/1000`;

		if (count > 800) {
			this.charCount.style.color = "#e74c3c";
		} else if (count > 600) {
			this.charCount.style.color = "#f39c12";
		} else {
			this.charCount.style.color = "#6c757d";
		}
	}

	updateSendButton() {
		const hasText = this.messageInput.value.trim().length > 0;
		this.sendButton.disabled = !hasText;
	}

	formatTime(date) {
		return date.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
	}

	scrollToBottom() {
		this.chatMessages.scrollTop = this.chatMessages.scrollHeight;
	}

	showLoading() {
		this.loadingOverlay.classList.add("show");
	}

	hideLoading() {
		this.loadingOverlay.classList.remove("show");
	}
}

// Global error handler to suppress extension-related errors
window.addEventListener("error", (event) => {
	if (
		event.message &&
		event.message.includes("Could not establish connection")
	) {
		// Suppress browser extension connection errors
		event.preventDefault();
		return false;
	}
});

// Handle unhandled promise rejections
window.addEventListener("unhandledrejection", (event) => {
	if (
		event.reason &&
		event.reason.message &&
		event.reason.message.includes("Could not establish connection")
	) {
		// Suppress browser extension connection errors
		event.preventDefault();
		return false;
	}
});

// Initialize app when DOM is loaded
let app;
document.addEventListener("DOMContentLoaded", () => {
	app = new RAGChatApp();
});

// Handle page visibility change to check connection
document.addEventListener("visibilitychange", () => {
	if (!document.hidden && app) {
		setTimeout(() => {
			app.checkConnection();
		}, 1000);
	}
});

// Global function for download all functionality
window.downloadAllSearchResults = function () {
	if (
		!window.currentSearchResults ||
		window.currentSearchResults.length === 0
	) {
		alert("No search results to download");
		return;
	}

	const apiUrl = window.location.origin;
	let downloadCount = 0;
	const totalFiles = window.currentSearchResults.length;

	// Show progress
	const progressDiv = document.createElement("div");
	progressDiv.className = "download-progress";
	progressDiv.innerHTML = `
		<div class="progress-header">
			<i class="fas fa-download"></i>
			<span>Downloading ${totalFiles} files...</span>
		</div>
		<div class="progress-bar">
			<div class="progress-fill" style="width: 0%"></div>
		</div>
		<div class="progress-text">0 / ${totalFiles}</div>
	`;

	// Add progress to the last message
	const lastMessage = document.querySelector(
		".message:last-child .message-content"
	);
	if (lastMessage) {
		lastMessage.appendChild(progressDiv);
	}

	// Download each file
	window.currentSearchResults.forEach((source, index) => {
		const link = document.createElement("a");
		link.href = `${apiUrl}/files/${source.document_id}/${source.filename}`;
		link.download = source.filename;
		link.target = "_blank";

		// Add event listener for successful download
		link.addEventListener("click", () => {
			downloadCount++;
			const progressFill = progressDiv.querySelector(".progress-fill");
			const progressText = progressDiv.querySelector(".progress-text");

			if (progressFill) {
				progressFill.style.width = `${(downloadCount / totalFiles) * 100}%`;
			}
			if (progressText) {
				progressText.textContent = `${downloadCount} / ${totalFiles}`;
			}

			if (downloadCount === totalFiles) {
				setTimeout(() => {
					progressDiv.innerHTML = `
						<div class="progress-complete">
							<i class="fas fa-check-circle"></i>
							<span>All ${totalFiles} files downloaded successfully!</span>
						</div>
					`;
				}, 1000);
			}
		});

		// Trigger download
		document.body.appendChild(link);
		link.click();
		document.body.removeChild(link);
	});
};
