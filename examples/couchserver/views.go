package main

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"

	"code.google.com/p/dsallings-couch-go"
)

const groupsjson = `{
   "_id": "_design/groups",
   "language": "javascript",
   "views": {
       "list": {
           "map": "function(doc) {\n  if (doc.type === \"group\") {\n    emit(doc._id, [doc.description, 0, 0, 0]);\n  } else if (doc.type === \"article\") {\n    var groups = doc.headers[\"Newsgroups\"][0].split(\",\")\n    for (var i = 0; i < groups.length; i++) {\n        var g = groups[i]\n        emit(g, [\"\", 1, doc.nums[g], doc.nums[g]]);\n    }\n  }\n}\n",
           "reduce": "function (key, values) {\n    var result = [\"\", 0, 0, 0];\n\n    values.forEach(function(p) {\n        if (p[0].length > result[0].length) {\n            result[0] = p[0];\n        }\n        result[1] += p[1];\n\tresult[2] = Math.min(result[2], p[2]);\n\tresult[3] = Math.max(result[3], p[3]);\n        // Dumb special case\n        if (result[2] === 0 && result[1] != 0) {\n          result[2] = 1;\n        }\n    });\n\n    return result;\n}"
       }
   }
}`

const articlesjson = `{
   "_id": "_design/articles",
   "language": "javascript",
   "views": {
       "list": {
           "map": "function(doc) {\n  if (doc.type === \"article\") {\n    for (var g in doc.nums) {\n        emit([g, doc.nums[g]], null);\n    }\n  }\n}\n",
           "reduce": "_count"
       }
   }
}`

func viewUpdateOK(i int) bool {
	return i == 200 || i == 409
}

func updateView(db *couch.Database, viewdata string) error {
	r, err := http.Post(db.DBURL(), "application/json", strings.NewReader(viewdata))
	if r == nil {
		defer r.Body.Close()
	} else {
		return err
	}
	if !viewUpdateOK(r.StatusCode) {
		return errors.New(fmt.Sprintf("Error updating view:  %v", r.Status))
	}
	return nil
}

func ensureViews(db *couch.Database) error {
	errg := updateView(db, groupsjson)
	if errg != nil {
		log.Printf("Error creating groups view %v", errg)
	}

	erra := updateView(db, articlesjson)
	if erra != nil {
		log.Printf("Error creating articles view %v", erra)
	}

	if erra != nil || errg != nil {
		return errors.New("Error making views")
	}

	return nil
}
