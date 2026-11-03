package gitlab

import (
	"context"
	"fmt"
	"net/http"
	"testing"
)

func TestTrackedProjectLookupChecksIdentity(t *testing.T) {
	for _,id:=range []int64{42,43}{t.Run(fmt.Sprint(id),func(t *testing.T){
		client:=testClient(t,func(w http.ResponseWriter,r *http.Request){
			if r.URL.Path!="/api/v4/projects/42"||r.Header.Get("PRIVATE-TOKEN")!="test-token"{t.Error("unexpected lookup request")}
			fmt.Fprintf(w,`{"id":%d,"name":"api","path_with_namespace":"platform/api"}`,id)
		})
		project,err:=client.Project(context.Background(),"test-token",42)
		if id==42&&(err!=nil||project.ID!=42){t.Fatalf("lookup failed: %v",err)}
		if id!=42&&err==nil{t.Fatal("wrong project identity accepted")}
	})}
}
