variable "gitlab_token" {
  type        = string
  sensitive   = true
  nullable    = false
  description = "Supply as TF_VAR_gitlab_token; do not commit credentials."
}

variable "gitlab_base_url" {
  type        = string
  nullable    = false
  description = "GitLab API URL, including /api/v4/."
}

variable "infrastructure" {
  type = map(object({
    parent_group     = optional(string)
    description      = optional(string, "")
    visibility_level = optional(string, "private")
    members          = optional(map(string), {})
    projects = optional(map(object({
      description            = optional(string, "")
      visibility_level       = optional(string, "private")
      initialize_with_readme = optional(bool, true)
      default_branch         = optional(string, "main")
      merge_method           = optional(string, "merge")
      protected_branches = optional(map(object({
        push_access_level  = optional(string, "no one")
        merge_access_level = optional(string, "maintainer")
        allow_force_push   = optional(bool, false)
      })), {})
    })), {})
  }))
  nullable = false
}
