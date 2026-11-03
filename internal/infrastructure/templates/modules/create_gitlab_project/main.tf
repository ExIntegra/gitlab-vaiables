locals {
  projects = { for project in flatten([
    for group_name, group in var.infrastructure : [
      for project_name, project in group.projects : merge(project, {
        group_name = group_name
        name       = project_name
      })
    ]
  ]) : "${project.group_name}/${project.name}" => project }

  branch_rules = { for rule in flatten([
    for project_key, project in local.projects : [
      for branch, rule in project.protected_branches : merge(rule, {
        project_key = project_key
        branch      = branch
      })
    ]
  ]) : jsonencode([rule.project_key, rule.branch]) => rule }
}

resource "gitlab_project" "project" {
  for_each               = local.projects
  name                   = each.value.name
  path                   = each.value.name
  namespace_id           = var.group_ids[each.value.group_name]
  description            = each.value.description
  visibility_level       = each.value.visibility_level
  initialize_with_readme = each.value.initialize_with_readme
  default_branch         = each.value.initialize_with_readme ? each.value.default_branch : null
  merge_method           = each.value.merge_method
}

resource "gitlab_branch_protection" "rules" {
  for_each           = local.branch_rules
  project            = gitlab_project.project[each.value.project_key].id
  branch             = each.value.branch
  push_access_level  = each.value.push_access_level
  merge_access_level = each.value.merge_access_level
  allow_force_push   = each.value.allow_force_push
}

output "project_id" {
  value = { for name, project in gitlab_project.project : name => project.id }
}
