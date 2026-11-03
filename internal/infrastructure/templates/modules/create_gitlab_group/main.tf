data "gitlab_group" "parent" {
  for_each  = toset([for group in values(var.create_group) : group.parent_group if group.parent_group != null])
  full_path = each.value
}

resource "gitlab_group" "new_group" {
  for_each         = var.create_group
  name             = each.key
  path             = each.key
  parent_id        = each.value.parent_group == null ? null : data.gitlab_group.parent[each.value.parent_group].id
  description      = each.value.description
  visibility_level = each.value.visibility_level
}

output "group_id" {
  value = { for name, group in gitlab_group.new_group : name => group.id }
}
