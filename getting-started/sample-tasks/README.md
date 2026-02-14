# Sample Tasks

This directory contains example tasks that demonstrate Pilot's capabilities.

## Task Types

### 1. Simple Bug Fix
```markdown
# Fix validation error in user registration

**Issue:** User registration fails when email contains special characters

**Expected:** Email validation should accept valid email formats including special characters

**Steps to reproduce:**
1. Navigate to registration page
2. Enter email with '+' character (e.g., user+test@example.com)
3. Submit form
4. Observe validation error

**Acceptance criteria:**
- [ ] Email validation accepts RFC-compliant email formats
- [ ] Unit tests added for edge cases
- [ ] Registration flow works with special character emails
```

### 2. Feature Addition
```markdown
# Add rate limiting to API endpoints

**Description:** Implement rate limiting to prevent API abuse and ensure service stability

**Requirements:**
- 100 requests per minute per IP address
- 1000 requests per hour per authenticated user
- Proper HTTP status codes (429 Too Many Requests)
- Rate limit headers in responses

**Acceptance criteria:**
- [ ] Rate limiting middleware implemented
- [ ] Redis-based rate limiting storage
- [ ] Unit and integration tests
- [ ] Documentation updated
- [ ] Monitoring alerts configured
```

### 3. Refactoring Task
```markdown
# Refactor authentication middleware for better testability

**Context:** Current auth middleware is tightly coupled and difficult to test

**Goals:**
- Extract interface for token validation
- Add dependency injection
- Improve error handling
- Increase test coverage to >90%

**Approach:**
- Create TokenValidator interface
- Implement JWT and mock implementations
- Update middleware to use injected validator
- Add comprehensive unit tests

**Success criteria:**
- [ ] Interface extracted and implemented
- [ ] All existing functionality preserved
- [ ] Test coverage >90%
- [ ] No breaking changes to API
```